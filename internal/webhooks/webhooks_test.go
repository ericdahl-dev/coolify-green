package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ericdahl-dev/coolify-green/internal/config"
)

func TestDispatchPostsSignedJSON(t *testing.T) {
	var (
		mu       sync.Mutex
		gotBody  []byte
		gotSig   string
		gotType  string
		received = make(chan struct{}, 1)
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotSig, gotType = body, r.Header.Get(SignatureHeader), r.Header.Get("Content-Type")
		mu.Unlock()
		received <- struct{}{}
	}))
	defer srv.Close()

	d := New([]config.Webhook{{URL: srv.URL, Secret: "shh"}})
	evt := Event{
		Event:        EventDeploymentStuck,
		Reason:       ReasonDeployInProgress,
		Instance:     "studio",
		Project:      "Brandywine Coins",
		ResourceType: "application",
		Resource:     "brandywinecoins",
		StuckSince:   time.Now().Add(-time.Hour),
		Timestamp:    time.Now(),
	}
	d.Dispatch(evt)

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("webhook was not delivered")
	}

	mu.Lock()
	defer mu.Unlock()
	if gotType != "application/json" {
		t.Errorf("content-type = %q", gotType)
	}
	var decoded Event
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if decoded.Resource != "brandywinecoins" || decoded.Reason != ReasonDeployInProgress {
		t.Errorf("decoded = %+v", decoded)
	}
	mac := hmac.New(sha256.New, []byte("shh"))
	mac.Write(gotBody)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); gotSig != want {
		t.Errorf("signature = %q, want %q", gotSig, want)
	}
}

func TestDispatchWithoutSecretSendsNoSignature(t *testing.T) {
	done := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		done <- r.Header.Get(SignatureHeader)
	}))
	defer srv.Close()

	New([]config.Webhook{{URL: srv.URL}}).Dispatch(Event{Event: EventResourceStuck})
	select {
	case sig := <-done:
		if sig != "" {
			t.Errorf("unsigned request carried a signature: %q", sig)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook was not delivered")
	}
}

func TestDispatchSurvivesFailingEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	// Must not panic and must not block.
	New([]config.Webhook{{URL: srv.URL}}).Dispatch(Event{Event: EventResourceStuck})
}

func TestDispatchNoHooksIsNoop(t *testing.T) {
	New(nil).Dispatch(Event{Event: EventResourceStuck})
}
