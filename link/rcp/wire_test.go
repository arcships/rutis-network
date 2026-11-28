package rcp

import (
	"bytes"
	"crypto/ecdh"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/flynn/noise"
)

var updateVector = flag.Bool("update-rcp-vector", false, "regenerate the public RCP/1 test vector")

func credentials(t *testing.T) (Credentials, Credentials) {
	t.Helper()
	makeCred := func(role string, seed byte) Credentials {
		// These deliberately recognizable scalar bytes are public fixture inputs.
		return Credentials{Role: role, PrivateKey: bytes.Repeat([]byte{seed}, 32), Binding: []byte("public-vector-1"), ExpiresAt: time.Now().Add(time.Hour)}
	}
	client, daemon := makeCred("client", 0x11), makeCred("daemon", 0x22)
	public := func(key []byte) []byte {
		k, e := ecdh.X25519().NewPrivateKey(key)
		if e != nil {
			t.Fatal(e)
		}
		return k.PublicKey().Bytes()
	}
	client.PeerKey = public(daemon.PrivateKey)
	daemon.PeerKey = public(client.PrivateKey)
	return client, daemon
}

func TestWireVector(t *testing.T) {
	client, daemon := credentials(t)
	ephem1, ephem2 := bytes.Repeat([]byte{0x33}, 32), bytes.Repeat([]byte{0x44}, 32)
	state := func(c Credentials, ephem []byte) *noise.HandshakeState {
		hs, err := pairState(c, bytes.NewReader(ephem))
		if err != nil {
			t.Fatal(err)
		}
		return hs
	}
	clientHS, daemonHS := state(client, ephem1), state(daemon, ephem2)
	a, b := offer(1024, 2*time.Second), offer(2048, time.Second)
	first, _, _, err := clientHS.WriteMessage(nil, a)
	if err != nil {
		t.Fatal(err)
	}
	got, _, _, err := daemonHS.ReadMessage(nil, first)
	if err != nil || !bytes.Equal(got, a) {
		t.Fatal("client offer", err)
	}
	second, rx, tx, err := daemonHS.WriteMessage(nil, b)
	if err != nil {
		t.Fatal(err)
	}
	got, clientTx, clientRx, err := clientHS.ReadMessage(nil, second)
	if err != nil || !bytes.Equal(got, b) {
		t.Fatal("daemon offer", err)
	}
	h := hex.EncodeToString
	vector := map[string]string{
		"binding": h(client.Binding), "clientStaticPrivate": h(client.PrivateKey), "daemonStaticPrivate": h(daemon.PrivateKey), "clientEphemeralPrivate": h(ephem1), "daemonEphemeralPrivate": h(ephem2),
		"clientOffer": h(a), "daemonOffer": h(b), "clientPair": h(append([]byte(version), first...)), "daemonPair": h(append([]byte(version), second...)),
	}
	check := func(name string, sender, receiver *noise.CipherState, kind byte, plain []byte) {
		t.Helper()
		frame, err := seal(sender, kind, plain)
		if err != nil {
			t.Fatal(err)
		}
		corrupt := bytes.Clone(frame)
		corrupt[len(corrupt)-1] ^= 1
		if name == "clientData" {
			vector["clientDataInvalidTag"] = h(corrupt)
		}
		if _, _, err := unseal(receiver, corrupt); err != badFrame {
			t.Fatal("accepted modified tag")
		}
		k, p, err := unseal(receiver, frame)
		if err != nil || k != kind || !bytes.Equal(p, plain) {
			t.Fatal(name, err)
		}
		if _, _, err := unseal(receiver, frame); err != badFrame {
			t.Fatal("accepted replay")
		}
		vector[name] = h(frame)
	}
	check("clientReady", clientTx, rx, ready, nil)
	check("daemonReady", tx, clientRx, ready, nil)
	vector["plain"] = h([]byte("hello"))
	check("clientData", clientTx, rx, dataMessage, []byte("hello"))
	check("daemonEmptyData", tx, clientRx, dataMessage, nil)
	check("clientClose", clientTx, rx, closeDone, nil)
	vector["rejectVersion"] = h([]byte(rejectVersion))
	vector["rejectCredential"] = h([]byte(rejectCredential))
	out, err := json.MarshalIndent(vector, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, '\n')
	const path = "testdata/wire-v1.json"
	if *updateVector {
		if err = os.WriteFile(path, out, 0644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(out, expected) {
		t.Fatal("wire vector changed", err)
	}
}
