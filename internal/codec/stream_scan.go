package codec

import (
	"bufio"
	"io"
	"strings"
)

func scanSSEData(body io.Reader, onData func(string) error) error {
	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		if line == "" && err == io.EOF {
			return nil
		}
		line = strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data != "" && data != "[DONE]" {
				if cbErr := onData(data); cbErr != nil {
					return cbErr
				}
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}
