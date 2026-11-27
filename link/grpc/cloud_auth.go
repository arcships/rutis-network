package grpclink

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type CloudMember struct {
	Tenant    string `json:"tenant"`
	ID        string `json:"id"`
	CertName  string `json:"certName,omitempty"`
	KeySHA256 string `json:"keySHA256,omitempty"`
	Subject   string `json:"subject,omitempty"`
}

type JWTPolicy struct {
	Issuer        string `json:"issuer"`
	Audience      string `json:"audience"`
	Scope         string `json:"scope"`
	PublicKeyFile string `json:"publicKeyFile"`
}

type memberID struct{ tenant, id string }
type cloudAuth struct {
	certs     map[string]memberID
	keys      map[string]memberID
	subjects  map[string]memberID
	jwt       *JWTPolicy
	publicKey *rsa.PublicKey
}

func label(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func validAddress(addr string) bool {
	host, p, err := net.SplitHostPort(addr)
	port, e := strconv.Atoi(p)
	return err == nil && host != "" && e == nil && port > 0 && port <= 65535
}
func newCloudAuth(members []CloudMember, policy *JWTPolicy) (*cloudAuth, error) {
	a := &cloudAuth{certs: map[string]memberID{}, keys: map[string]memberID{}, subjects: map[string]memberID{}, jwt: policy}
	if policy != nil {
		copy := *policy
		a.jwt = &copy
		if policy.Issuer == "" || policy.Audience == "" || policy.Scope == "" {
			return nil, errors.New("JWT issuer, audience and scope are required")
		}
		b, err := os.ReadFile(policy.PublicKeyFile)
		if err != nil {
			return nil, err
		}
		a.publicKey, err = jwt.ParseRSAPublicKeyFromPEM(b)
		if err != nil {
			return nil, err
		}
	}
	for _, m := range members {
		if m.CertName == "" && m.KeySHA256 == "" && m.Subject == "" {
			return nil, errors.New("member has no admission credential")
		}
		id := memberID{m.Tenant, m.ID}
		if m.KeySHA256 != "" {
			b, err := hex.DecodeString(m.KeySHA256)
			if err != nil || len(b) != sha256.Size {
				return nil, errors.New("invalid API key digest")
			}
		}
		if m.Subject != "" && policy == nil {
			return nil, errors.New("JWT subject requires a JWT policy")
		}
		for _, binding := range []struct {
			k     string
			index map[string]memberID
		}{{strings.ToLower(m.CertName), a.certs}, {strings.ToLower(m.KeySHA256), a.keys}, {m.Subject, a.subjects}} {
			if binding.k == "" {
				continue
			}
			if _, exists := binding.index[binding.k]; exists {
				return nil, errors.New("ambiguous member credential")
			}
			binding.index[binding.k] = id
		}
	}
	return a, nil
}

func verifiedTLS(ctx context.Context) (credentials.TLSInfo, bool) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return credentials.TLSInfo{}, false
	}
	t, ok := p.AuthInfo.(credentials.TLSInfo)
	return t, ok
}
func clientCertificate(ctx context.Context) *x509.Certificate {
	t, ok := verifiedTLS(ctx)
	if !ok || len(t.State.VerifiedChains) == 0 {
		return nil
	}
	return t.State.VerifiedChains[0][0]
}
func certificateHash(ctx context.Context) []byte {
	if cert := clientCertificate(ctx); cert != nil {
		sum := sha256.Sum256(cert.Raw)
		return sum[:]
	}
	return nil
}
func namedCert(ctx context.Context, name string) bool {
	if cert := clientCertificate(ctx); cert != nil {
		for _, n := range cert.DNSNames {
			if strings.EqualFold(n, name) {
				return true
			}
		}
	}
	return false
}
func (a *cloudAuth) member(ctx context.Context) (memberID, error) {
	if _, ok := verifiedTLS(ctx); !ok {
		return memberID{}, status.Error(codes.Unauthenticated, "TLS required")
	}
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 0 {
		return a.bearer(values)
	}
	cert := clientCertificate(ctx)
	if cert == nil {
		return memberID{}, status.Error(codes.Unauthenticated, "member credential required")
	}
	var found memberID
	for _, n := range cert.DNSNames {
		if id, ok := a.certs[strings.ToLower(n)]; ok {
			if found.id != "" && found != id {
				return memberID{}, status.Error(codes.PermissionDenied, "ambiguous certificate")
			}
			found = id
		}
	}
	if found.id != "" {
		return found, nil
	}
	return memberID{}, status.Error(codes.PermissionDenied, "certificate denied")
}

func (a *cloudAuth) bearer(values []string) (memberID, error) {
	invalid := status.Error(codes.Unauthenticated, "invalid bearer credential")
	if len(values) != 1 {
		return memberID{}, invalid
	}
	token, ok := strings.CutPrefix(values[0], "Bearer ")
	if !ok || len(token) == 0 || len(token) > 16384 {
		return memberID{}, invalid
	}
	sum := sha256.Sum256([]byte(token))
	if id, ok := a.keys[hex.EncodeToString(sum[:])]; ok {
		return id, nil
	}
	if a.jwt == nil {
		return memberID{}, invalid
	}
	var claims struct {
		jwt.RegisteredClaims
		Scope string `json:"scope"`
	}
	_, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) { return a.publicKey, nil },
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(a.jwt.Issuer),
		jwt.WithAudience(a.jwt.Audience), jwt.WithExpirationRequired())
	if err != nil {
		return memberID{}, invalid
	}
	id, known := a.subjects[claims.Subject]
	if !known || !slices.Contains(strings.Fields(claims.Scope), a.jwt.Scope) {
		return memberID{}, status.Error(codes.PermissionDenied, "subject or scope denied")
	}
	return id, nil
}
