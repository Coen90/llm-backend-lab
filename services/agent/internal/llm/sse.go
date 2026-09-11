package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const maxEventBytes = 1 << 20

// readResponseEvent reads one complete SSE event and decodes its JSON data.
// It skips comments and joins data lines until a blank line ends the event.
func readResponseEvent(ctx context.Context, scanner *bufio.Scanner) (responseEvent, error) {
	var data strings.Builder
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return responseEvent{}, err
		}
		line := strings.TrimPrefix(scanner.Text(), "\uFEFF") // LF or CRLF; allow a UTF-8 BOM.
		if line == "" {
			if data.Len() == 0 {
				continue
			}
			var event responseEvent
			if err := json.Unmarshal([]byte(data.String()), &event); err != nil || event.Type == "" {
				return responseEvent{}, ErrInvalidResponse
			}
			return event, nil
		}
		field, value, _ := strings.Cut(line, ":")
		if field != "data" {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		if data.Len()+len(value)+1 > maxEventBytes {
			return responseEvent{}, ErrInvalidResponse
		}
		data.WriteString(value)
		data.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return responseEvent{}, fmt.Errorf("read stream: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return responseEvent{}, err
	}
	// The caller stops at a terminal event, so EOF here means an unfinished response.
	return responseEvent{}, ErrInvalidResponse
}
