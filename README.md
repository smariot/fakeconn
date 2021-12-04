# fakeconn

Record and replay a go net.Conn, mostly for testing and debugging.

This package isn't finished, so you probably shouldn't use it.

## Recording

```go
fakeconn.Record(c net.Conn, w io.Writer) net.Conn
```

This function wraps the passed connection, and logs any the data written to or read from to the passed writer.

It also records errors, only io.EOF is recorded faithfully. Errors regarding i/o deadlines, and reading from a closed connection are ignored, and all others have only their error string recorded.

Consecutive reads and writes are combined, and writing them
may be delayed until the connection is closed.

## Playback

```go
fakeconn.Replay(r io.Reader) net.Conn
```

Creates a fake connection that replays the events read from the passed reader.

Reads will block until the any prerequisite writes have occured.

Writes that don't match the recorded events will generate errors.

## Data Format

**Warning**: This format is likely to change.

Each event is on its own line, and and is composed of UTF-8 text:

```plaintext
# This is a comment.
# Blank lines are also ignored.

# This is data that can be read from the connection:
<- data

# data must be valid UTF-8 text.
# binary data must be escaped.
# the following escapes are can be used:
#
#  \\         backslash
#  \r         cartridge return
#  \n         newline
#  \x..       raw byte
#  \u....     unicode code point
#  \U........ unicode code point

# This is data that needs to be written to
# the connection.
-> more data

# Note that writing different data will result in an
# error, and the write will block while data is being read.

# Here is more data to read, but again,
# the read will block until the previous writes
# have been completed.
<- yet more data

# This closes the remote side of the connection.
# Any reads from the connection after this point
# will return io.EOF
<-!EOF

# This closes the local side of the connection.
# any writes to the connection after this point
# will return an error with the message "example".
->!ERR example

# The end of the file is marks where the connection
# is expected to be closed.
#
# Reads at this point will block, and
# writes at this point will raise an error, as no
# further data was expected.
```

Here is an example of an HTTP connection, from the perspective of the server:

```plaintext
<- GET / HTTP/1.1\r\n
<- Host: 127.0.0.1:80\r\n
<- User-Agent: Go-http-client/1.1\r\n
<- Accept-Encoding: gzip\r\n
<- \r\n
-> HTTP/1.1 200 OK\r\n
-> Date: Fri, 03 Dec 2021 00:00:00 GMT\r\n
-> Content-Length: 13\r\n
-> Content-Type: text/plain; charset=utf-8\r\n
-> \r\n
-> Hello, World!
```