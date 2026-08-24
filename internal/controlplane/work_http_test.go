package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestWorkAnswerHTTPStoresTrustedContext(t *testing.T) {
	store, _, _, work := needsInputWork(t)
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)
	input := protocol.WorkAnswerRequest{
		RequestID: "64000000-0000-4000-8000-000000000001",
		Message:   "Preserve the existing response shape.",
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost,
		server.URL+"/api/v1/work/"+work.ID+"/answer", bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var answer protocol.WorkAnswer
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&answer) != nil ||
		answer.WorkID != work.ID || answer.Message != input.Message {
		t.Fatalf("answer response = %d %#v", response.StatusCode, answer)
	}
}
