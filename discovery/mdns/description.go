package mdns

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const offerType = "_rutis-link._tcp"
const maxTXT = 400
const maxParams = 64 << 10

var offerID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,63}$`)
var offerKind = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,95}$`)

// Description contains only public parameters chosen by the protocol adapter.
type Description struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Version int             `json:"version"`
	Params  json.RawMessage `json:"params"`
}

// SourceSnapshot is the return type of a local source's snapshot and waitForChange.
type SourceSnapshot struct {
	Cursor string        `json:"cursor"`
	Offers []Description `json:"offers"`
}

type envelope struct {
	id, kind, rev string
	version       int
	inline        string
}

func (e envelope) valid() bool {
	sum, err := hex.DecodeString(e.rev)
	return offerID.MatchString(e.id) && offerKind.MatchString(e.kind) && e.version > 0 && e.version <= 2147483647 && err == nil && len(sum) == sha256.Size && strings.ToLower(e.rev) == e.rev
}

func (e envelope) path() string { return "/v1/offers/" + e.id + "/" + e.rev }

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func object(data []byte) bool {
	trim := bytes.TrimSpace(data)
	return len(data) <= maxParams && len(trim) > 0 && trim[0] == '{' && utf8.Valid(data) && json.Valid(trim)
}

func (e envelope) decode(data []byte) (*Description, error) {
	if digest(data) != e.rev || !object(data) {
		return nil, errors.New("invalid offer data or revision")
	}
	return &Description{ID: e.id, Kind: e.kind, Version: e.version, Params: json.RawMessage(data)}, nil
}

type encodedOffer struct {
	envelope
	txt, body []byte
}

func encodeOffer(d Description) (encodedOffer, error) {
	if !object(d.Params) {
		return encodedOffer{}, fmt.Errorf("offer %q requires a JSON object of at most %d bytes", d.ID, maxParams)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, d.Params); err != nil {
		return encodedOffer{}, err
	}
	data := buf.Bytes()
	e := envelope{id: d.ID, kind: d.Kind, version: d.Version, rev: digest(data)}
	if !e.valid() {
		return encodedOffer{}, errors.New("invalid offer id, kind or version")
	}
	var txt []byte
	for _, s := range []string{"txtvers=1", "id=" + e.id, "kind=" + e.kind, "v=" + strconv.Itoa(e.version), "rev=" + e.rev} {
		txt = append(txt, byte(len(s)))
		txt = append(txt, s...)
	}
	if len(data)+2 <= 255 && len(txt)+3+len(data) <= maxTXT {
		txt = append(txt, byte(len(data)+2), 'p', '=')
		txt = append(txt, data...)
	}
	return encodedOffer{e, txt, data}, nil
}

func parseEnvelope(txt string) (*envelope, bool) {
	if len(txt) > maxTXT {
		return nil, false
	}
	fields := make(map[string]string)
	for len(txt) > 0 {
		n := int(txt[0])
		if n == 0 || n >= len(txt) {
			return nil, false
		}
		k, v, ok := strings.Cut(txt[1:1+n], "=")
		txt = txt[1+n:]
		k = strings.ToLower(k)
		if !ok {
			continue
		}
		if _, dup := fields[k]; dup {
			return nil, false
		}
		fields[k] = v
	}
	v, err := strconv.Atoi(fields["v"])
	e := envelope{id: fields["id"], kind: fields["kind"], version: v, rev: fields["rev"], inline: fields["p"]}
	if err != nil || fields["txtvers"] != "1" || !e.valid() {
		return nil, false
	}
	if _, ok := fields["p"]; ok && e.inline == "" {
		return nil, false
	}
	return &e, true
}
