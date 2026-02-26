// +build windows

package pipe

import (
	"net"
	"path/filepath"
	"time"

	npipe "gopkg.in/natefinch/npipe.v2"
)

func GetPipeSocket(path string) (net.Conn, error) {
	path = filepath.FromSlash(path)
	return npipe.DialTimeout(path, time.Second*5)
}
