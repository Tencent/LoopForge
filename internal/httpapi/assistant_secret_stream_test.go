package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestIssuedGitTokensAreRedactedAcrossStreamDeltas(t *testing.T) {
	const token = "fake+stream/token=12345678"
	gitSecretRedactions.add("cnb", token)
	for _, form := range []string{token, url.QueryEscape(token), base64.StdEncoding.EncodeToString([]byte("cnb:" + token))} {
		for _, kind := range []string{"answer", "thinking", "draftDelta"} {
			t.Run(kind+"/"+url.QueryEscape(form), func(t *testing.T) {
				var stream assistantSecretStream
				var projection assistantStreamProjection
				var delivered strings.Builder
				publish := func(events []assistantStreamEvent) {
					for _, event := range events {
						data, _ := json.Marshal(event)
						projection.add(data)
						if kind == "draftDelta" {
							delivered.WriteString(event.Detail)
						} else {
							delivered.WriteString(event.Text)
						}
					}
				}
				for _, character := range "before " + form + " after" {
					event := assistantStreamEvent{Kind: kind, Text: string(character)}
					if kind == "draftDelta" {
						event.Detail = event.Text
						event.Text = ""
					}
					publish(stream.push(event, domain.User{}))
				}
				publish(stream.flush(domain.User{}))
				if strings.Contains(delivered.String(), form) || !strings.Contains(delivered.String(), "[redacted]") {
					t.Fatalf("unsafe or missing redaction: %s", delivered.String())
				}
				if kind == "answer" && strings.Contains(projection.snapshot(1).Answer, token) {
					t.Fatal("generation projection exposed the token")
				}
			})
		}
	}
}

func TestSecretStreamPreservesOrdinaryPrefixesAndResets(t *testing.T) {
	u := domain.User{GitToken: "something-secret"}
	var stream assistantSecretStream
	var got strings.Builder
	for _, piece := range []string{"ordinary ", "some"} {
		for _, event := range stream.push(assistantStreamEvent{Kind: "answer", Text: piece}, u) {
			got.WriteString(event.Text)
		}
	}
	for _, event := range stream.flush(u) {
		got.WriteString(event.Text)
	}
	if got.String() != "ordinary some" {
		t.Fatalf("lost ordinary text: %q", got.String())
	}
	stream.push(assistantStreamEvent{Kind: "answer", Text: "some"}, u)
	stream.push(assistantStreamEvent{Kind: "answerReset"}, u)
	for _, event := range stream.flush(u) {
		if event.Kind == "answer" {
			t.Fatal("reset answer retained a buffered prefix")
		}
	}
}
