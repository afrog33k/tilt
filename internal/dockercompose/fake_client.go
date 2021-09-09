package dockercompose

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/compose-spec/compose-go/loader"
	"github.com/stretchr/testify/require"

	"github.com/compose-spec/compose-go/types"

	"github.com/tilt-dev/tilt/internal/container"
	"github.com/tilt-dev/tilt/pkg/model"
)

type FakeDCClient struct {
	t   *testing.T
	ctx context.Context

	RunLogOutput      map[model.TargetName]<-chan string
	ContainerIdOutput container.ID
	eventJson         chan string
	ConfigOutput      string

	UpCalls   []UpCall
	DownError error
	WorkDir   string
}

var _ DockerComposeClient = &FakeDCClient{}

// Represents a single call to Up
type UpCall struct {
	PathToConfig []string
	ServiceName  model.TargetName
	ShouldBuild  bool
}

func NewFakeDockerComposeClient(t *testing.T, ctx context.Context) *FakeDCClient {
	return &FakeDCClient{
		t:            t,
		ctx:          ctx,
		eventJson:    make(chan string, 100),
		RunLogOutput: make(map[model.TargetName]<-chan string),
	}
}

func (c *FakeDCClient) Up(ctx context.Context, configPaths []string, serviceName model.TargetName,
	shouldBuild bool, stdout, stderr io.Writer) error {
	c.UpCalls = append(c.UpCalls, UpCall{configPaths, serviceName, shouldBuild})
	return nil
}

func (c *FakeDCClient) Down(ctx context.Context, configPaths []string, stdout, stderr io.Writer) error {
	if c.DownError != nil {
		err := c.DownError
		c.DownError = err
		return err
	}
	return nil
}

func (c *FakeDCClient) StreamLogs(ctx context.Context, _ []string, serviceName model.TargetName) io.ReadCloser {
	output := c.RunLogOutput[serviceName]
	reader, writer := io.Pipe()
	go func() {
		c.t.Helper()

		// docker-compose always logs an "Attaching to foo, bar" at the start of a log session
		_, err := writer.Write([]byte(fmt.Sprintf("Attaching to %s\n", serviceName)))
		require.NoError(c.t, err, "Failed to write to fake Docker Compose logs")

		done := false
		for !done {
			select {
			case <-ctx.Done():
				done = true
			case s, ok := <-output:
				if !ok {
					done = true
				} else {
					logLine := fmt.Sprintf("%s %s\n",
						time.Now().Format(time.RFC3339Nano),
						strings.TrimRightFunc(s, unicode.IsSpace))
					_, err = writer.Write([]byte(logLine))
					require.NoError(c.t, err, "Failed to write to fake Docker Compose logs")
				}
			}
		}

		// we call docker-compose logs with --follow, so it only terminates (normally) when the container exits
		// and it writes a message with the container exit code
		_, err = writer.Write([]byte(fmt.Sprintf("%s exited with code 0\n", serviceName)))
		require.NoError(c.t, err, "Failed to write to fake Docker Compose logs")
		require.NoError(c.t, writer.Close(), "Failed to close fake Docker Compose logs writer")
	}()
	return reader
}

func (c *FakeDCClient) StreamEvents(ctx context.Context, configPaths []string) (<-chan string, error) {
	events := make(chan string, 10)
	go func() {
		for {
			select {
			case event := <-c.eventJson:
				select {
				case events <- event: // send event to channel (unless it's full)
				default:
					panic(fmt.Sprintf("no room on events channel to send event: '%s'. Something "+
						"is wrong (or you need to increase the buffer).", event))
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return events, nil
}

func (c *FakeDCClient) SendEvent(evt Event) error {
	j, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	c.eventJson <- string(j)
	return nil
}

func (c *FakeDCClient) Config(_ context.Context, _ []string) (string, error) {
	return c.ConfigOutput, nil
}

func (c *FakeDCClient) Project(_ context.Context, _ []string) (*types.Project, error) {
	return loader.Load(types.ConfigDetails{
		WorkingDir: c.WorkDir,
		ConfigFiles: []types.ConfigFile{
			{
				Content: []byte(c.ConfigOutput),
			},
		},
	})
}

func (c *FakeDCClient) ContainerID(ctx context.Context, configPaths []string, serviceName model.TargetName) (container.ID, error) {
	return c.ContainerIdOutput, nil
}
