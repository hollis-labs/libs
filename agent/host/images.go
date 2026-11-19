package host

import (
	"encoding/base64"
	"errors"
	"sync"

	"github.com/hollis-labs/libs/llm/core/llmtypes"
)

// Pictures a tool hands back (a look at the screen, say). The thread keeps a tool result
// as text, so a picture is held here beside it, by the call it answers, and put into the
// request when the thread is sent. Only the most recent few go with a request; an older
// one stays in the thread as its text. They are not kept across a restart.

// Image is one picture, as the Messages API takes it.
type Image struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"` // base64
}

const (
	maxImageBytes  = 1500 << 10 // of base64
	maxCallImages  = 3
	keptImageCalls = 12 // calls whose pictures are held
	sentImageCalls = 3  // calls whose pictures go with a request
)

type imageStore struct {
	mu     sync.Mutex
	byCall map[string][]Image
	order  []string
}

func newImageStore() *imageStore { return &imageStore{byCall: map[string][]Image{}} }

// validImages checks what the page sent: a few pictures, of a kind the model takes, not huge.
func validImages(list []Image) error {
	if len(list) > maxCallImages {
		return errors.New("too many pictures in one result")
	}
	for _, im := range list {
		switch im.MediaType {
		case "image/jpeg", "image/png", "image/webp", "image/gif":
		default:
			return errors.New("a picture must be JPEG, PNG, WebP or GIF")
		}
		if len(im.Data) == 0 || len(im.Data) > maxImageBytes {
			return errors.New("a picture is empty or too large")
		}
		if _, err := base64.StdEncoding.DecodeString(im.Data); err != nil {
			return errors.New("a picture is not base64")
		}
	}
	return nil
}

func (s *imageStore) keep(callID string, list []Image) {
	if len(list) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byCall[callID] = list
	s.order = append(s.order, callID)
	for len(s.order) > keptImageCalls {
		delete(s.byCall, s.order[0])
		s.order = s.order[1:]
	}
}

// shown picks the pictures that go with a request: those of the last few tool results in
// the history that have any.
func (s *imageStore) shown(in []llmtypes.ChatMessage) map[string][]Image {
	out := map[string][]Image{}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.byCall) == 0 {
		return out
	}
	for i := len(in) - 1; i >= 0 && len(out) < sentImageCalls; i-- {
		for j := len(in[i].ContentBlocks) - 1; j >= 0 && len(out) < sentImageCalls; j-- {
			b := in[i].ContentBlocks[j]
			if b.Type != "tool_result" {
				continue
			}
			if list := s.byCall[b.ToolUseID]; len(list) > 0 {
				out[b.ToolUseID] = list
			}
		}
	}
	return out
}

// resultContent is a tool result as it is sent: its text alone, or its text and pictures.
func resultContent(text string, images []Image) any {
	if len(images) == 0 {
		return text
	}
	blocks := []wireBlock{{Type: "text", Text: text}}
	for _, im := range images {
		blocks = append(blocks, wireBlock{Type: "image", Source: &wireSource{Type: "base64", MediaType: im.MediaType, Data: im.Data}})
	}
	return blocks
}
