// Copyright 2015, Google Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package grpctmclient

import (
	"fmt"
	"io"
	"time"

	"google.golang.org/grpc"

	mproto "github.com/youtube/vitess/go/mysql/proto"
	"github.com/youtube/vitess/go/rpcwrap/bsonrpc"
	blproto "github.com/youtube/vitess/go/vt/binlog/proto"
	"github.com/youtube/vitess/go/vt/hook"
	"github.com/youtube/vitess/go/vt/logutil"
	myproto "github.com/youtube/vitess/go/vt/mysqlctl/proto"
	"github.com/youtube/vitess/go/vt/rpc"
	"github.com/youtube/vitess/go/vt/tabletmanager/actionnode"
	"github.com/youtube/vitess/go/vt/tabletmanager/gorpcproto"
	"github.com/youtube/vitess/go/vt/tabletmanager/tmclient"
	"github.com/youtube/vitess/go/vt/topo"
	"golang.org/x/net/context"

	pb "github.com/youtube/vitess/go/vt/proto/tabletmanagerdata"
	pbs "github.com/youtube/vitess/go/vt/proto/tabletmanagerservice"
)

type timeoutError struct {
	error
}

func init() {
	tmclient.RegisterTabletManagerClientFactory("grpc", func() tmclient.TabletManagerClient {
		return &Client{}
	})
}

// Client implements tmclient.TabletManagerClient
type Client struct{}

// dial returns a client to use
func (client *Client) dial(ctx context.Context, tablet *topo.TabletInfo) (*grpc.ClientConn, pbs.TabletManagerClient, error) {
	// create the RPC client, using ctx.Deadline if set, or no timeout.
	var connectTimeout time.Duration
	deadline, ok := ctx.Deadline()
	if ok {
		connectTimeout = deadline.Sub(time.Now())
		if connectTimeout < 0 {
			return nil, nil, timeoutError{fmt.Errorf("timeout connecting to TabletManager on %v", tablet.Alias)}
		}
	}

	var cc *grpc.ClientConn
	var err error
	if connectTimeout == 0 {
		cc, err = grpc.Dial(tablet.Addr(), grpc.WithBlock())
	} else {
		cc, err = grpc.Dial(tablet.Addr(), grpc.WithBlock(), grpc.WithTimeout(connectTimeout))
	}
	if err != nil {
		return nil, nil, err
	}
	return cc, pbs.NewTabletManagerClient(cc), nil
}

// rpcCallTablet wil execute the RPC on the remote server.
func (client *Client) rpcCallTablet(ctx context.Context, tablet *topo.TabletInfo, name string, args, reply interface{}) error {
	// create the RPC client, using ctx.Deadline if set, or no timeout.
	var connectTimeout time.Duration
	deadline, ok := ctx.Deadline()
	if ok {
		connectTimeout = deadline.Sub(time.Now())
		if connectTimeout < 0 {
			return timeoutError{fmt.Errorf("timeout connecting to TabletManager.%v on %v", name, tablet.Alias)}
		}
	}
	rpcClient, err := bsonrpc.DialHTTP("tcp", tablet.Addr(), connectTimeout, nil)
	if err != nil {
		return fmt.Errorf("RPC error for %v: %v", tablet.Alias, err.Error())
	}
	defer rpcClient.Close()

	// use the context Done() channel. Will handle context timeout.
	call := rpcClient.Go(ctx, "TabletManager."+name, args, reply, nil)
	select {
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			return timeoutError{fmt.Errorf("timeout waiting for TabletManager.%v to %v", name, tablet.Alias)}
		}
		return fmt.Errorf("interrupted waiting for TabletManager.%v to %v", name, tablet.Alias)
	case <-call.Done:
		if call.Error != nil {
			return fmt.Errorf("remote error for %v: %v", tablet.Alias, call.Error.Error())
		}
		return nil
	}
}

//
// Various read-only methods
//

// Ping is part of the tmclient.TabletManagerClient interface
func (client *Client) Ping(ctx context.Context, tablet *topo.TabletInfo) error {
	cc, c, err := client.dial(ctx, tablet)
	if err != nil {
		return err
	}
	defer cc.Close()
	result, err := c.Ping(ctx, &pb.PingRequest{
		Payload: "payload",
	})
	if err != nil {
		return err
	}
	if result.Payload != "payload" {
		return fmt.Errorf("bad ping result: %v", result.Payload)
	}
	return nil
}

// Sleep is part of the tmclient.TabletManagerClient interface
func (client *Client) Sleep(ctx context.Context, tablet *topo.TabletInfo, duration time.Duration) error {
	cc, c, err := client.dial(ctx, tablet)
	if err != nil {
		return err
	}
	defer cc.Close()
	_, err = c.Sleep(ctx, &pb.SleepRequest{
		Duration: int64(duration),
	})
	return err
}

// ExecuteHook is part of the tmclient.TabletManagerClient interface
func (client *Client) ExecuteHook(ctx context.Context, tablet *topo.TabletInfo, hk *hook.Hook) (*hook.HookResult, error) {
	cc, c, err := client.dial(ctx, tablet)
	if err != nil {
		return nil, err
	}
	defer cc.Close()
	hr, err := c.ExecuteHook(ctx, &pb.ExecuteHookRequest{
		Name:       hk.Name,
		Parameters: hk.Parameters,
		ExtraEnv:   hk.ExtraEnv,
	})
	if err != nil {
		return nil, err
	}
	return &hook.HookResult{
		ExitStatus: int(hr.ExitStatus),
		Stdout:     hr.Stdout,
		Stderr:     hr.Stderr,
	}, nil
}

// GetSchema is part of the tmclient.TabletManagerClient interface
func (client *Client) GetSchema(ctx context.Context, tablet *topo.TabletInfo, tables, excludeTables []string, includeViews bool) (*myproto.SchemaDefinition, error) {
	cc, c, err := client.dial(ctx, tablet)
	if err != nil {
		return nil, err
	}
	defer cc.Close()
	response, err := c.GetSchema(ctx, &pb.GetSchemaRequest{
		Tables:        tables,
		ExcludeTables: excludeTables,
		IncludeViews:  includeViews,
	})
	if err != nil {
		return nil, err
	}
	return myproto.ProtoToSchemaDefinition(response.SchemaDefinition), nil
}

// GetPermissions is part of the tmclient.TabletManagerClient interface
func (client *Client) GetPermissions(ctx context.Context, tablet *topo.TabletInfo) (*myproto.Permissions, error) {
	cc, c, err := client.dial(ctx, tablet)
	if err != nil {
		return nil, err
	}
	defer cc.Close()
	response, err := c.GetPermissions(ctx, &pb.GetPermissionsRequest{})
	if err != nil {
		return nil, err
	}
	return myproto.ProtoToPermissions(response.Permissions), nil
}

//
// Various read-write methods
//

// SetReadOnly is part of the tmclient.TabletManagerClient interface
func (client *Client) SetReadOnly(ctx context.Context, tablet *topo.TabletInfo) error {
	cc, c, err := client.dial(ctx, tablet)
	if err != nil {
		return err
	}
	defer cc.Close()
	_, err = c.SetReadOnly(ctx, &pb.SetReadOnlyRequest{})
	return err
}

// SetReadWrite is part of the tmclient.TabletManagerClient interface
func (client *Client) SetReadWrite(ctx context.Context, tablet *topo.TabletInfo) error {
	cc, c, err := client.dial(ctx, tablet)
	if err != nil {
		return err
	}
	defer cc.Close()
	_, err = c.SetReadWrite(ctx, &pb.SetReadWriteRequest{})
	return err
}

// ChangeType is part of the tmclient.TabletManagerClient interface
func (client *Client) ChangeType(ctx context.Context, tablet *topo.TabletInfo, dbType topo.TabletType) error {
	cc, c, err := client.dial(ctx, tablet)
	if err != nil {
		return err
	}
	defer cc.Close()
	_, err = c.ChangeType(ctx, &pb.ChangeTypeRequest{
		TabletType: topo.TabletTypeToProto(dbType),
	})
	return err
}

// Scrap is part of the tmclient.TabletManagerClient interface
func (client *Client) Scrap(ctx context.Context, tablet *topo.TabletInfo) error {
	cc, c, err := client.dial(ctx, tablet)
	if err != nil {
		return err
	}
	defer cc.Close()
	_, err = c.Scrap(ctx, &pb.ScrapRequest{})
	return err
}

// RefreshState is part of the tmclient.TabletManagerClient interface
func (client *Client) RefreshState(ctx context.Context, tablet *topo.TabletInfo) error {
	cc, c, err := client.dial(ctx, tablet)
	if err != nil {
		return err
	}
	defer cc.Close()
	_, err = c.RefreshState(ctx, &pb.RefreshStateRequest{})
	return err
}

// RunHealthCheck is part of the tmclient.TabletManagerClient interface
func (client *Client) RunHealthCheck(ctx context.Context, tablet *topo.TabletInfo, targetTabletType topo.TabletType) error {
	cc, c, err := client.dial(ctx, tablet)
	if err != nil {
		return err
	}
	defer cc.Close()
	_, err = c.RunHealthCheck(ctx, &pb.RunHealthCheckRequest{
		TabletType: topo.TabletTypeToProto(targetTabletType),
	})
	return err
}

// HealthStream is part of the tmclient.TabletManagerClient interface
func (client *Client) HealthStream(ctx context.Context, tablet *topo.TabletInfo) (<-chan *actionnode.HealthStreamReply, tmclient.ErrFunc, error) {
	cc, c, err := client.dial(ctx, tablet)
	if err != nil {
		return nil, nil, err
	}

	logstream := make(chan *actionnode.HealthStreamReply, 10)
	stream, err := c.StreamHealth(ctx, &pb.StreamHealthRequest{})
	if err != nil {
		cc.Close()
		return nil, nil, err
	}

	var finalErr error
	go func() {
		for {
			shr, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					finalErr = err
				}
				close(logstream)
				return
			}
			logstream <- &actionnode.HealthStreamReply{
				Tablet:              topo.ProtoToTablet(shr.Tablet),
				BinlogPlayerMapSize: shr.BinlogPlayerMapSize,
				HealthError:         shr.HealthError,
				ReplicationDelay:    time.Duration(shr.ReplicationDelay),
			}
		}
	}()
	return logstream, func() error {
		cc.Close()
		return finalErr
	}, nil
}

// ReloadSchema is part of the tmclient.TabletManagerClient interface
func (client *Client) ReloadSchema(ctx context.Context, tablet *topo.TabletInfo) error {
	cc, c, err := client.dial(ctx, tablet)
	if err != nil {
		return err
	}
	defer cc.Close()
	_, err = c.ReloadSchema(ctx, &pb.ReloadSchemaRequest{})
	return err
}

// PreflightSchema is part of the tmclient.TabletManagerClient interface
func (client *Client) PreflightSchema(ctx context.Context, tablet *topo.TabletInfo, change string) (*myproto.SchemaChangeResult, error) {
	var scr myproto.SchemaChangeResult
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionPreflightSchema, change, &scr); err != nil {
		return nil, err
	}
	return &scr, nil
}

// ApplySchema is part of the tmclient.TabletManagerClient interface
func (client *Client) ApplySchema(ctx context.Context, tablet *topo.TabletInfo, change *myproto.SchemaChange) (*myproto.SchemaChangeResult, error) {
	var scr myproto.SchemaChangeResult
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionApplySchema, change, &scr); err != nil {
		return nil, err
	}
	return &scr, nil
}

// ExecuteFetchAsDba is part of the tmclient.TabletManagerClient interface
func (client *Client) ExecuteFetchAsDba(ctx context.Context, tablet *topo.TabletInfo, query string, maxRows int, wantFields, disableBinlogs, reloadSchema bool) (*mproto.QueryResult, error) {
	var qr mproto.QueryResult
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionExecuteFetchAsDba, &gorpcproto.ExecuteFetchArgs{
		Query:          query,
		DbName:         tablet.DbName(),
		MaxRows:        maxRows,
		WantFields:     wantFields,
		DisableBinlogs: disableBinlogs,
		ReloadSchema:   reloadSchema,
	}, &qr); err != nil {
		return nil, err
	}
	return &qr, nil
}

// ExecuteFetchAsApp is part of the tmclient.TabletManagerClient interface
func (client *Client) ExecuteFetchAsApp(ctx context.Context, tablet *topo.TabletInfo, query string, maxRows int, wantFields bool) (*mproto.QueryResult, error) {
	var qr mproto.QueryResult
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionExecuteFetchAsApp, &gorpcproto.ExecuteFetchArgs{
		Query:      query,
		MaxRows:    maxRows,
		WantFields: wantFields,
	}, &qr); err != nil {
		return nil, err
	}
	return &qr, nil
}

//
// Replication related methods
//

// SlaveStatus is part of the tmclient.TabletManagerClient interface
func (client *Client) SlaveStatus(ctx context.Context, tablet *topo.TabletInfo) (myproto.ReplicationStatus, error) {
	var status myproto.ReplicationStatus
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionSlaveStatus, &rpc.Unused{}, &status); err != nil {
		return myproto.ReplicationStatus{}, err
	}
	return status, nil
}

// MasterPosition is part of the tmclient.TabletManagerClient interface
func (client *Client) MasterPosition(ctx context.Context, tablet *topo.TabletInfo) (myproto.ReplicationPosition, error) {
	var rp myproto.ReplicationPosition
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionMasterPosition, &rpc.Unused{}, &rp); err != nil {
		return rp, err
	}
	return rp, nil
}

// StopSlave is part of the tmclient.TabletManagerClient interface
func (client *Client) StopSlave(ctx context.Context, tablet *topo.TabletInfo) error {
	return client.rpcCallTablet(ctx, tablet, actionnode.TabletActionStopSlave, &rpc.Unused{}, &rpc.Unused{})
}

// StopSlaveMinimum is part of the tmclient.TabletManagerClient interface
func (client *Client) StopSlaveMinimum(ctx context.Context, tablet *topo.TabletInfo, minPos myproto.ReplicationPosition, waitTime time.Duration) (myproto.ReplicationPosition, error) {
	var pos myproto.ReplicationPosition
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionStopSlaveMinimum, &gorpcproto.StopSlaveMinimumArgs{
		Position: minPos,
		WaitTime: waitTime,
	}, &pos); err != nil {
		return pos, err
	}
	return pos, nil
}

// StartSlave is part of the tmclient.TabletManagerClient interface
func (client *Client) StartSlave(ctx context.Context, tablet *topo.TabletInfo) error {
	return client.rpcCallTablet(ctx, tablet, actionnode.TabletActionStartSlave, &rpc.Unused{}, &rpc.Unused{})
}

// TabletExternallyReparented is part of the tmclient.TabletManagerClient interface
func (client *Client) TabletExternallyReparented(ctx context.Context, tablet *topo.TabletInfo, externalID string) error {
	return client.rpcCallTablet(ctx, tablet, actionnode.TabletActionExternallyReparented, &gorpcproto.TabletExternallyReparentedArgs{ExternalID: externalID}, &rpc.Unused{})
}

// GetSlaves is part of the tmclient.TabletManagerClient interface
func (client *Client) GetSlaves(ctx context.Context, tablet *topo.TabletInfo) ([]string, error) {
	var sl gorpcproto.GetSlavesReply
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionGetSlaves, &rpc.Unused{}, &sl); err != nil {
		return nil, err
	}
	return sl.Addrs, nil
}

// WaitBlpPosition is part of the tmclient.TabletManagerClient interface
func (client *Client) WaitBlpPosition(ctx context.Context, tablet *topo.TabletInfo, blpPosition blproto.BlpPosition, waitTime time.Duration) error {
	return client.rpcCallTablet(ctx, tablet, actionnode.TabletActionWaitBLPPosition, &gorpcproto.WaitBlpPositionArgs{
		BlpPosition: blpPosition,
		WaitTimeout: waitTime,
	}, &rpc.Unused{})
}

// StopBlp is part of the tmclient.TabletManagerClient interface
func (client *Client) StopBlp(ctx context.Context, tablet *topo.TabletInfo) (*blproto.BlpPositionList, error) {
	var bpl blproto.BlpPositionList
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionStopBLP, &rpc.Unused{}, &bpl); err != nil {
		return nil, err
	}
	return &bpl, nil
}

// StartBlp is part of the tmclient.TabletManagerClient interface
func (client *Client) StartBlp(ctx context.Context, tablet *topo.TabletInfo) error {
	return client.rpcCallTablet(ctx, tablet, actionnode.TabletActionStartBLP, &rpc.Unused{}, &rpc.Unused{})
}

// RunBlpUntil is part of the tmclient.TabletManagerClient interface
func (client *Client) RunBlpUntil(ctx context.Context, tablet *topo.TabletInfo, positions *blproto.BlpPositionList, waitTime time.Duration) (myproto.ReplicationPosition, error) {
	var pos myproto.ReplicationPosition
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionRunBLPUntil, &gorpcproto.RunBlpUntilArgs{
		BlpPositionList: positions,
		WaitTimeout:     waitTime,
	}, &pos); err != nil {
		return myproto.ReplicationPosition{}, err
	}
	return pos, nil
}

//
// Reparenting related functions
//

// ResetReplication is part of the tmclient.TabletManagerClient interface
func (client *Client) ResetReplication(ctx context.Context, tablet *topo.TabletInfo) error {
	return client.rpcCallTablet(ctx, tablet, actionnode.TabletActionResetReplication, &rpc.Unused{}, &rpc.Unused{})
}

// InitMaster is part of the tmclient.TabletManagerClient interface
func (client *Client) InitMaster(ctx context.Context, tablet *topo.TabletInfo) (myproto.ReplicationPosition, error) {
	var rp myproto.ReplicationPosition
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionInitMaster, &rpc.Unused{}, &rp); err != nil {
		return myproto.ReplicationPosition{}, err
	}
	return rp, nil
}

// PopulateReparentJournal is part of the tmclient.TabletManagerClient interface
func (client *Client) PopulateReparentJournal(ctx context.Context, tablet *topo.TabletInfo, timeCreatedNS int64, actionName string, masterAlias topo.TabletAlias, pos myproto.ReplicationPosition) error {
	args := &gorpcproto.PopulateReparentJournalArgs{
		TimeCreatedNS:       timeCreatedNS,
		ActionName:          actionName,
		MasterAlias:         masterAlias,
		ReplicationPosition: pos,
	}
	return client.rpcCallTablet(ctx, tablet, actionnode.TabletActionPopulateReparentJournal, args, &rpc.Unused{})
}

// InitSlave is part of the tmclient.TabletManagerClient interface
func (client *Client) InitSlave(ctx context.Context, tablet *topo.TabletInfo, parent topo.TabletAlias, replicationPosition myproto.ReplicationPosition, timeCreatedNS int64) error {
	args := &gorpcproto.InitSlaveArgs{
		Parent:              parent,
		ReplicationPosition: replicationPosition,
		TimeCreatedNS:       timeCreatedNS,
	}
	deadline, ok := ctx.Deadline()
	if ok {
		args.WaitTimeout = deadline.Sub(time.Now())
		if args.WaitTimeout < 0 {
			return timeoutError{fmt.Errorf("timeout connecting to TabletManager.InitSlave on %v", tablet.Alias)}
		}
	}

	return client.rpcCallTablet(ctx, tablet, actionnode.TabletActionInitSlave, args, &rpc.Unused{})
}

// DemoteMaster is part of the tmclient.TabletManagerClient interface
func (client *Client) DemoteMaster(ctx context.Context, tablet *topo.TabletInfo) (myproto.ReplicationPosition, error) {
	var rp myproto.ReplicationPosition
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionDemoteMaster, &rpc.Unused{}, &rp); err != nil {
		return rp, err
	}
	return rp, nil
}

// PromoteSlaveWhenCaughtUp is part of the tmclient.TabletManagerClient interface
func (client *Client) PromoteSlaveWhenCaughtUp(ctx context.Context, tablet *topo.TabletInfo, pos myproto.ReplicationPosition) (myproto.ReplicationPosition, error) {
	var rp myproto.ReplicationPosition
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionPromoteSlaveWhenCaughtUp, &pos, &rp); err != nil {
		return rp, err
	}
	return rp, nil
}

// SlaveWasPromoted is part of the tmclient.TabletManagerClient interface
func (client *Client) SlaveWasPromoted(ctx context.Context, tablet *topo.TabletInfo) error {
	return client.rpcCallTablet(ctx, tablet, actionnode.TabletActionSlaveWasPromoted, &rpc.Unused{}, &rpc.Unused{})
}

// SetMaster is part of the tmclient.TabletManagerClient interface
func (client *Client) SetMaster(ctx context.Context, tablet *topo.TabletInfo, parent topo.TabletAlias, timeCreatedNS int64, forceStartSlave bool) error {
	args := &gorpcproto.SetMasterArgs{
		Parent:          parent,
		TimeCreatedNS:   timeCreatedNS,
		ForceStartSlave: forceStartSlave,
	}
	deadline, ok := ctx.Deadline()
	if ok {
		args.WaitTimeout = deadline.Sub(time.Now())
		if args.WaitTimeout < 0 {
			return timeoutError{fmt.Errorf("timeout connecting to TabletManager.SetMaster on %v", tablet.Alias)}
		}
	}

	return client.rpcCallTablet(ctx, tablet, actionnode.TabletActionSetMaster, args, &rpc.Unused{})
}

// SlaveWasRestarted is part of the tmclient.TabletManagerClient interface
func (client *Client) SlaveWasRestarted(ctx context.Context, tablet *topo.TabletInfo, args *actionnode.SlaveWasRestartedArgs) error {
	return client.rpcCallTablet(ctx, tablet, actionnode.TabletActionSlaveWasRestarted, args, &rpc.Unused{})
}

// StopReplicationAndGetStatus is part of the tmclient.TabletManagerClient interface
func (client *Client) StopReplicationAndGetStatus(ctx context.Context, tablet *topo.TabletInfo) (myproto.ReplicationStatus, error) {
	var rp myproto.ReplicationStatus
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionStopReplicationAndGetStatus, &rpc.Unused{}, &rp); err != nil {
		return rp, err
	}
	return rp, nil
}

// PromoteSlave is part of the tmclient.TabletManagerClient interface
func (client *Client) PromoteSlave(ctx context.Context, tablet *topo.TabletInfo) (myproto.ReplicationPosition, error) {
	var rp myproto.ReplicationPosition
	if err := client.rpcCallTablet(ctx, tablet, actionnode.TabletActionPromoteSlave, &rpc.Unused{}, &rp); err != nil {
		return rp, err
	}
	return rp, nil
}

//
// Backup related methods
//

// Backup is part of the tmclient.TabletManagerClient interface
func (client *Client) Backup(ctx context.Context, tablet *topo.TabletInfo, concurrency int) (<-chan *logutil.LoggerEvent, tmclient.ErrFunc, error) {
	var connectTimeout time.Duration
	deadline, ok := ctx.Deadline()
	if ok {
		connectTimeout = deadline.Sub(time.Now())
		if connectTimeout < 0 {
			return nil, nil, timeoutError{fmt.Errorf("timeout connecting to TabletManager.Backup on %v", tablet.Alias)}
		}
	}
	rpcClient, err := bsonrpc.DialHTTP("tcp", tablet.Addr(), connectTimeout, nil)
	if err != nil {
		return nil, nil, err
	}

	logstream := make(chan *logutil.LoggerEvent, 10)
	rpcstream := make(chan *logutil.LoggerEvent, 10)
	c := rpcClient.StreamGo("TabletManager.Backup", &gorpcproto.BackupArgs{
		Concurrency: concurrency,
	}, rpcstream)
	interrupted := false
	go func() {
		for {
			select {
			case <-ctx.Done():
				// context is done
				interrupted = true
				close(logstream)
				rpcClient.Close()
				return
			case ssr, ok := <-rpcstream:
				if !ok {
					close(logstream)
					rpcClient.Close()
					return
				}
				logstream <- ssr
			}
		}
	}()
	return logstream, func() error {
		// this is only called after streaming is done
		if interrupted {
			return fmt.Errorf("TabletManager.Backup interrupted by context")
		}
		return c.Error
	}, nil
}

//
// RPC related methods
//

// IsTimeoutError is part of the tmclient.TabletManagerClient interface
func (client *Client) IsTimeoutError(err error) bool {
	switch err.(type) {
	case timeoutError:
		return true
	default:
		return false
	}
}
