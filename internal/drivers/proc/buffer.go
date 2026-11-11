package proc

import (
	"bytes"
	"fmt"
)

// headTail keeps the first and the last bytes of a stream, so a chatty
// script loses its middle, not its start (what it tried) or its end
// (why it failed).
//
//	[ head: max/2 ] … N bytes cut … [ tail: max/2 ]
type headTail struct {
	half int
	head []byte
	tail []byte
	cut  int
}

func newHeadTail(max int) *headTail {
	return &headTail{half: max / 2}
}

func (b *headTail) Write(p []byte) (int, error) {
	n := len(p)

	if room := b.half - len(b.head); room > 0 {
		take := min(room, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}

	b.tail = append(b.tail, p...)
	if over := len(b.tail) - b.half; over > 0 {
		b.cut += over
		copy(b.tail, b.tail[over:])
		b.tail = b.tail[:b.half]
	}

	return n, nil
}

// String joins head and tail. When bytes were cut, both ends are trimmed
// to whole lines where a newline exists, and a marker line sits between.
func (b *headTail) String() string {
	if b.cut == 0 {
		return string(b.head) + string(b.tail)
	}

	head, tail, cut := b.head, b.tail, b.cut

	if i := bytes.LastIndexByte(head, '\n'); i >= 0 {
		cut += len(head) - (i + 1)
		head = head[:i+1]
	}
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		cut += i + 1
		tail = tail[i+1:]
	}

	sep := ""
	if len(head) > 0 && head[len(head)-1] != '\n' {
		sep = "\n"
	}

	return fmt.Sprintf("%s%s… %d bytes cut …\n%s", head, sep, cut, tail)
}
