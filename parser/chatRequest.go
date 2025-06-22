package parser

import (
	objs "github.com/hamidteimouri/telego/objects"
)

type chatRequestHandler struct {
	requestId int
	function  *func(*objs.Update)
}
