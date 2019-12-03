package net

type Error string

func (e Error) Error() string { return string(e) }

const ErrServerClosed = Error("listen socket was closed")
