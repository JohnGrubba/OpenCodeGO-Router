package usage

import (
	"bytes"
	"encoding/json"
)

// Collector incrementally parses server-sent events and accumulates usage.
type Collector struct {
	family string
	usage  Usage
	carry  []byte
	max    int
}

func NewCollector(family string) *Collector {
	return &Collector{family: family, max: 8 << 20}
}

func (c *Collector) Feed(p []byte) {
	c.carry = append(c.carry, p...)
	for {
		idx, sep := eventBoundary(c.carry)
		if idx < 0 {
			break
		}
		event := c.carry[:idx]
		c.carry = c.carry[idx+sep:]
		c.handleEvent(event)
	}
	if len(c.carry) > c.max {
		c.carry = append([]byte(nil), c.carry[len(c.carry)-c.max:]...)
	}
}

func (c *Collector) handleEvent(event []byte) {
	var data bytes.Buffer
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		v := bytes.TrimPrefix(line, []byte("data:"))
		if len(v) > 0 && v[0] == ' ' {
			v = v[1:]
		}
		if data.Len() > 0 {
			data.WriteByte('\n')
		}
		data.Write(v)
	}
	raw := bytes.TrimSpace(data.Bytes())
	if len(raw) == 0 || bytes.Equal(raw, []byte("[DONE]")) {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return
	}
	var one Usage
	walk(v, &one)
	c.usage.Merge(one)
}

func eventBoundary(b []byte) (int, int) {
	i1 := bytes.Index(b, []byte("\n\n"))
	i2 := bytes.Index(b, []byte("\r\n\r\n"))
	switch {
	case i1 < 0 && i2 < 0:
		return -1, 0
	case i2 < 0 || (i1 >= 0 && i1 <= i2):
		return i1, 2
	default:
		return i2, 4
	}
}

// Usage returns the accumulated usage. Call Finalize afterwards.
func (c *Collector) Usage() Usage { return c.usage }

// Pending reports whether the stream had unparsed bytes left.
func (c *Collector) Pending() bool { return len(bytes.TrimSpace(c.carry)) > 0 }
