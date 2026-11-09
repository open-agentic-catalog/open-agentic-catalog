package main

import (
	"errors"
	"net"
	"testing"
)

type recordedConn struct {
	net.Conn
	closes  int
	failure error
}

func (c *recordedConn) Close() error { c.closes++; return c.failure }

type controlledListener struct {
	net.Listener
	accept func() (net.Conn, error)
}

func (l *controlledListener) Accept() (net.Conn, error) { return l.accept() }

func TestBoundedListenerAdmission(t *testing.T) {
	first, rejected, next := &recordedConn{}, &recordedConn{}, &recordedConn{}
	exhausted := errors.New("owned listener exhausted")
	var held net.Conn
	calls := 0
	l := &boundedListener{slots: make(chan struct{}, 1)}
	l.Listener = &controlledListener{accept: func() (net.Conn, error) {
		calls++
		switch calls {
		case 1:
			return first, nil
		case 2:
			return rejected, nil
		case 3:
			if rejected.closes != 1 || len(l.slots) != 1 {
				t.Fatal("excess connection was not refused at full capacity")
			}
			if err := held.Close(); err != nil {
				t.Fatal(err)
			}
			return next, nil
		case 4:
			return nil, exhausted
		default:
			t.Fatal("listener error did not terminate admission")
			return nil, exhausted
		}
	}}
	var err error
	held, err = l.Accept()
	if err != nil || len(l.slots) != 1 || first.closes != 0 {
		t.Fatal("first connection not reserved", err)
	}
	admitted, err := l.Accept()
	if err != nil || len(l.slots) != 1 || first.closes != 1 || next.closes != 0 {
		t.Fatal("released capacity not reused", err)
	}
	if err := admitted.Close(); err != nil {
		t.Fatal(err)
	}
	if len(l.slots) != 0 || next.closes != 1 {
		t.Fatal("admitted connection did not release capacity")
	}
	if _, err := l.Accept(); !errors.Is(err, exhausted) || calls != 4 {
		t.Fatal("listener failure was lost", err)
	}
}

func TestBoundedConnectionReleasesOnce(t *testing.T) {
	for _, underlyingError := range []error{nil, errors.New("owned close failure")} {
		connection := &recordedConn{failure: underlyingError}
		releases := 0
		c := &boundedConn{Conn: connection, release: func() { releases++ }}
		for range 2 {
			if err := c.Close(); !errors.Is(err, underlyingError) {
				t.Fatal("underlying close result changed", err)
			}
		}
		if releases != 1 || connection.closes != 2 {
			t.Fatal("slot release and underlying Close ownership changed", releases, connection.closes)
		}
	}
}
