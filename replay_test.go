package fakeconn

import (
	"fmt"
	"io"
	"strings"
)

func ExampleReplay() {
	events := `
# This is a comment.
<- Hello\n
<- \u2665World
<-!EOF
-> 12
-> 3
`

	c := Replay(strings.NewReader(events))

	var (
		len int
		err error
		buf [256]byte
	)

	for err == nil {
		len, err = c.Read(buf[:])
		fmt.Printf("read:  data=%q err=%v\n", buf[:len], err)
	}

	len, err = io.WriteString(c, "123456")
	fmt.Printf("write: size=%d err=%v\n", len, err)

	err = c.Close()
	fmt.Printf("close: err=%v\n", err)

	// Output:
	// read:  data="Hello\n" err=<nil>
	// read:  data="♥World" err=<nil>
	// read:  data="" err=EOF
	// write: size=3 err=write fake local->remote: line 8: bad write: data="123456" expected="123"
	// close: err=line 8: bad write: data="123456" expected="123"
}
