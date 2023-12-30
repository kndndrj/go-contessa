package contessa

import "net"

// GetFreePort asks the kernel for a free open port that is ready to use.
//
// taken from: github.com/phayes/freeport
func GetFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port, nil
}

// GetFreePortOr tries to get a free port from system kernel
// and returns the fallback value otherwise
func GetFreePortOr(fallback int) int {
	port, err := GetFreePort()
	if err != nil {
		return fallback
	}
	return port
}
