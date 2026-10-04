package protocol

import (
	"bufio"
	"io"
	"net/http"
)

func ScanSSELines(resp *http.Response, onLine func([]byte) bool) error {
	return ScanSSELinesReader(resp.Body, onLine)
}

// ScanSSELinesReader scans SSE lines from an arbitrary reader (the response
// body may already be partially consumed and replayed through a wrapper).
func ScanSSELinesReader(body io.Reader, onLine func([]byte) bool) error {
	reader := bufio.NewReaderSize(body, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if !onLine(line) {
				return nil
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
