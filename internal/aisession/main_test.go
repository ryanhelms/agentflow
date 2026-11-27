package aisession_test

import (
	"os"
	"testing"

	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

func TestMain(m *testing.M) {
	jsonrpctest.RunChild()
	os.Exit(m.Run())
}
