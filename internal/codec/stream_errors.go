package codec

import "errors"

// ErrStreamTruncated indicates that an upstream stream ended before delivering
// its protocol-specific terminal event.
var ErrStreamTruncated = errors.New("upstream stream truncated before completion")
