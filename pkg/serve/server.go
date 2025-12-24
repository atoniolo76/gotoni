/*
Copyright © 2025 ALESSIO TONIOLO

server.go contains methods for orchestrating vLLM across a cluster
*/
package serve

type Server struct {
	IP              string
	PendingRequests int
	Engine          Engine
}

type Engine struct {
	CmdPrefix           string
	TensorParallelFlag  string
	TrustRemoteCodeFlag string
	TimeoutFlag         string
}

var VLLMEngine = &Engine{
	CmdPrefix:           "vllm",
	TensorParallelFlag:  "tensor-parallel-size",
	TrustRemoteCodeFlag: "trust-remote-code",
	TimeoutFlag:         "timeout",
}

func NewServer(IP string, engine Engine) *Server {
	return &Server{
		IP:              IP,
		PendingRequests: 0,
		Engine:          engine,
	}
}
