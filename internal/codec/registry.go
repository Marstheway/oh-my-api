package codec

import "fmt"

var registry = map[Format]Codec{}

func register(format Format, c Codec) {
	if format == "" {
		panic("codec register: empty format")
	}
	if c == nil {
		panic("codec register: nil codec")
	}
	registry[format] = c
}

func Get(format Format) (Codec, error) {
	c, ok := registry[format]
	if !ok {
		return nil, fmt.Errorf("codec not registered: %s", format)
	}
	return c, nil
}
