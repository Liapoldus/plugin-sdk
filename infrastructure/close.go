package infrastructure

import "io"

// closeResource centralizes best-effort cleanup for response bodies and
// streaming resources. Their primary operation already returned its result;
// cleanup errors cannot be reported without masking that result.
func closeResource(resource io.Closer) {
	if resource != nil {
		if err := resource.Close(); err != nil {
			return
		}
	}
}
