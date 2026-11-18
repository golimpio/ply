package metadata

import (
	"errors"
	"io"
	"net/http"
)

const maxResponseBytes = 8 << 20

var errResponseTooLarge = errors.New("response too large")

func readProviderResponse(resp *http.Response) (int, http.Header, []byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, nil, err
	}
	if len(body) > maxResponseBytes {
		return resp.StatusCode, resp.Header, nil, errResponseTooLarge
	}
	return resp.StatusCode, resp.Header, body, nil
}
