package gosdk

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	vmcp "github.com/frankbardon/verdict/server/mcp"
)

// registerResources mounts the resource catalogue. Resources rather than tools:
// the schema and the skill pack are documents an agent reads, not actions it
// takes, and a client that lists them should not have to weigh them against the
// tools it might call.
func registerResources(srv *mcpsdk.Server) {
	for _, rd := range vmcp.Resources() {
		read := rd.Read
		srv.AddResource(&mcpsdk.Resource{
			URI:         rd.URI,
			Name:        rd.Name,
			MIMEType:    rd.MIMEType,
			Description: rd.Description,
		}, func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			body, err := read(ctx)
			if err != nil {
				return nil, fmt.Errorf("verdict/mcp: reading %s: %w", req.Params.URI, err)
			}
			return &mcpsdk.ReadResourceResult{
				Contents: []*mcpsdk.ResourceContents{{
					URI:      req.Params.URI,
					MIMEType: rd.MIMEType,
					Text:     string(body),
				}},
			}, nil
		})
	}
}
