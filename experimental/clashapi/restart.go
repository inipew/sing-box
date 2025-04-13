package clashapi

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"runtime"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/service"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
	"golang.org/x/sys/unix"
)

func restartRouter(ctx context.Context, logFactory log.Factory) http.Handler {
	r := chi.NewRouter()
	r.Post("/", restart(ctx, logFactory))
	return r
}

func restart(ctx context.Context, logFactory log.Factory) func(w http.ResponseWriter, r *http.Request) {
	restartExecutable := func(execPath string) {

		inbound := service.FromContext[adapter.InboundManager](ctx)
		dnsTransport := service.FromContext[adapter.DNSTransportManager](ctx)
		common.Close(inbound, dnsTransport)

		logger := logFactory.Logger()
		logger.Info("sing-box restarting")

		var cmd *exec.Cmd
		var err error

		switch runtime.GOOS {
		case "windows":
			// Windows: use exec.Command and Start()
			cmd = exec.Command(execPath, os.Args[1:]...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			err = cmd.Start()
			if err != nil {
				logger.Error("sing-box restarting: ", err)
				return
			}
			os.Exit(0)
		case "android":
			// Android: use exec.Command approach with Start()
			// Android doesn't support syscall.Exec properly
			cmd = exec.Command(execPath, os.Args[1:]...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			// Set process group ID to avoid termination when parent exits
			cmd.SysProcAttr = &unix.SysProcAttr{
				Setpgid: true,
			}
			err = cmd.Start()
			if err != nil {
				logger.Error("sing-box restarting: ", err)
				return
			}
			os.Exit(0)
		default:
			// Linux and other Unix-like systems: use syscall.Exec
			err = unix.Exec(execPath, os.Args, os.Environ())
			if err != nil {
				logger.Error("sing-box restarting: ", err)
				return
			}
		}
	}

	return func(w http.ResponseWriter, r *http.Request) {
		execPath, err := os.Executable()
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, newError(err.Error()))
			return
		}

		go restartExecutable(execPath)

		render.JSON(w, r, render.M{"status": "ok"})
	}
}
