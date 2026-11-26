package proxysupport

import (
	"net/http"
	"strings"
)

func GeminiUpstreamHeaders(in http.Header, apiKey string, stream bool) map[string]string {
	out := UpstreamHeaders(in, apiKey)
	remove := map[string]bool{
		"x-goog-api-key": true, "proxy-authorization": true, "cookie": true,
		"connection": true, "proxy-connection": true, "keep-alive": true,
		"transfer-encoding": true, "te": true, "trailer": true, "upgrade": true,
		"content-encoding": true, "content-type": true, "accept": true,
		"x-http-method-override": true,
	}
	for k, values := range in {
		if strings.EqualFold(k, "connection") {
			for _, value := range values {
				for token := range strings.SplitSeq(value, ",") {
					remove[strings.ToLower(strings.TrimSpace(token))] = true
				}
			}
		}
	}
	for k := range out {
		if remove[strings.ToLower(k)] {
			delete(out, k)
		}
	}
	out["Authorization"] = "Bearer " + apiKey
	out["Accept-Encoding"] = "identity"
	out["Content-Type"] = "application/json"
	out["Accept"] = "application/json"
	if stream {
		out["Accept"] = "text/event-stream"
	}
	return out
}
