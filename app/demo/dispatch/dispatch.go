// Copyright 2020, Chef.  All rights reserved.
// https://github.com/q191201771/lal
//
// Use of this source code is governed by a MIT-style license
// that can be found in the License file.
//
// Author: Chef (191201771@qq.com)

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/q191201771/lal/app/demo/dispatch/datamanager"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/naza/pkg/nazahttp"
	"github.com/q191201771/naza/pkg/nazalog"
	"github.com/q191201771/naza/pkg/unique"
)

//
// 结合lalserver的HTTP Notify事件通知，以及HTTP API接口，
// 简单演示如何实现一个简单的调度服务，
// 使得多个lalserver节点可以组成一个集群，
// 集群内的所有节点功能都是相同的，
// 你可以将流推送至任意一个节点，并从任意一个节点拉流，
// 同一路流，推流和拉流可以在不同的节点。
//

var dataManager datamanager.DataManger

func OnPubStartHandler(w http.ResponseWriter, r *http.Request) {
	id := unique.GenUniqueKey("ReqID")

	var info base.PubStartInfo
	if err := nazahttp.UnmarshalRequestJsonBody(r, &info); err != nil {
		nazalog.Error(err)
		return
	}
	nazalog.Infof("[%s] on_pub_start. info=%+v", id, info)

	if _, exist := config.ServerId2Server[info.ServerId]; !exist {
		nazalog.Errorf("server id has not config. serverId=%s", info.ServerId)
		return
	}

	nazalog.Infof("add pub. streamName=%s, serverId=%s", info.StreamName, info.ServerId)
	dataManager.AddPub(info.StreamName, info.ServerId)
}

func OnPubStopHandler(w http.ResponseWriter, r *http.Request) {
	id := unique.GenUniqueKey("ReqID")

	var info base.PubStopInfo
	if err := nazahttp.UnmarshalRequestJsonBody(r, &info); err != nil {
		nazalog.Error(err)
		return
	}
	nazalog.Infof("[%s] on_pub_stop. info=%+v", id, info)

	if _, exist := config.ServerId2Server[info.ServerId]; !exist {
		nazalog.Errorf("server id has not config. serverId=%s", info.ServerId)
		return
	}

	nazalog.Infof("del pub. streamName=%s, serverId=%s", info.StreamName, info.ServerId)
	dataManager.DelPub(info.StreamName, info.ServerId)
}

func OnSubStartHandler(w http.ResponseWriter, r *http.Request) {
	id := unique.GenUniqueKey("ReqID")

	var info base.SubStartInfo
	if err := nazahttp.UnmarshalRequestJsonBody(r, &info); err != nil {
		nazalog.Error(err)
		return
	}
	nazalog.Infof("[%s] on_sub_start. info=%+v", id, info)

	// 演示通过流名称踢掉session，服务于鉴权等场景
	// 业务方真正使用时，可以通过流名称、用户IP、URL参数等信息，来判断是否需要踢掉session
	if info.StreamName == "cheftestkick" {
		kickSession(info.ServerId, info.StreamName, info.SessionId)
		return
	}

	// sub拉流时，判断是否需要触发pull级联拉流
	// 1. 是内部级联拉流，不需要触发
	if strings.Contains(info.UrlParam, config.PullSecretParam) {
		nazalog.Infof("[%s] sub is pull by other node, ignore.", id)
		return
	}
	// 2. 汇报的节点已经存在输入流，不需要触发
	if info.HasInSession {
		nazalog.Infof("[%s] has in session in the same node, ignore.", id)
		return
	}

	// 3. 非法节点，本服务没有配置汇报的节点
	reqServer, exist := config.ServerId2Server[info.ServerId]
	if !exist {
		nazalog.Errorf("[%s] req server id invalid.", id)
		return
	}

	pubServerId, exist := dataManager.QueryPub(info.StreamName)
	// 4. 没有查到流所在节点，不需要触发
	if !exist {
		nazalog.Infof("[%s] pub not exist, ignore.", id)
		return
	}

	pubServer, exist := config.ServerId2Server[pubServerId]
	nazalog.Assert(true, exist)

	// 向汇报节点，发送pull级联拉流的命令，其中包含pub所在节点信息
	// TODO(chef): start_relay_pull封装成函数，所有的http请求都应该封装成函数 202405
	// TODO(chef): 还没有测试新的接口start_relay_pull，只是保证可以编译通过
	url := fmt.Sprintf("http://%s/api/ctrl/start_relay_pull", reqServer.ApiAddr)
	var b base.ApiCtrlStartRelayPullReq
	b.Url = fmt.Sprintf("%s://%s/%s/%s?%s", "rtmp", pubServer.RtmpAddr, info.AppName, info.StreamName, config.PullSecretParam)
	//b.Protocol = base.ProtocolRtmp
	//b.Addr = pubServer.RtmpAddr
	//b.AppName = info.AppName
	//b.StreamName = info.StreamName
	//b.UrlParam = config.PullSecretParam

	nazalog.Infof("[%s] ctrl pull. send to %s with %+v", id, reqServer.ApiAddr, b)
	if _, err := nazahttp.PostJson(url, b, nil); err != nil {
		nazalog.Errorf("[%s] post json error. err=%+v", id, err)
	}
}

func OnSubStopHandler(w http.ResponseWriter, r *http.Request) {
	id := unique.GenUniqueKey("ReqID")

	var info base.SubStopInfo
	if err := nazahttp.UnmarshalRequestJsonBody(r, &info); err != nil {
		nazalog.Error(err)
		return
	}
	nazalog.Infof("[%s] on_sub_stop. info=%+v", id, info)
}

func OnUpdateHandler(w http.ResponseWriter, r *http.Request) {
	id := unique.GenUniqueKey("ReqID")

	var info base.UpdateInfo
	if err := nazahttp.UnmarshalRequestJsonBody(r, &info); err != nil {
		nazalog.Error(err)
		return
	}
	nazalog.Infof("[%s] on_update. info=%+v", id, info)

	var streamNameList []string
	for _, g := range info.Groups {
		// pub exist
		if g.StatPub.SessionId != "" {
			streamNameList = append(streamNameList, g.StreamName)
		}
	}
	dataManager.UpdatePub(info.ServerId, streamNameList)

	if config.MaxSubSessionPerIp > 0 {
		ip2SubSessions := make(map[string][]base.StatSub)
		sessionId2StreamName := make(map[string]string)
		for _, g := range info.Groups {
			for _, sub := range g.StatSubs {
				host, _, err := net.SplitHostPort(sub.RemoteAddr)
				if err != nil {
					nazalog.Warnf("split host port failed. remote addr=%s", sub.RemoteAddr)
					continue
				}
				ip2SubSessions[host] = append(ip2SubSessions[host], sub)
				sessionId2StreamName[sub.SessionId] = g.StreamName
			}
		}
		for ip, subs := range ip2SubSessions {
			if len(subs) > config.MaxSubSessionPerIp {
				nazalog.Debugf("close session. ip=%s, session count=%d", ip, len(subs))
				for _, sub := range subs {
					if sub.Protocol == base.SessionProtocolHlsStr {
						host, _, err := net.SplitHostPort(sub.RemoteAddr)
						if err != nil {
							nazalog.Warnf("split host port failed. remote addr=%s", sub.RemoteAddr)
							continue
						}
						addIpBlacklist(info.ServerId, host, 60)
					} else {
						kickSession(info.ServerId, sessionId2StreamName[sub.SessionId], sub.SessionId)
					}
				}
			}
		}
	}

	if config.MaxSubDurationSec > 0 {
		now := time.Now()
		for _, g := range info.Groups {
			for _, sub := range g.StatSubs {
				st, err := base.ParseReadableTime(sub.StartTime)
				if err != nil {
					nazalog.Warnf("parse readable time failed. start time=%s, err=%+v", sub.StartTime, err)
					continue
				}
				diff := int(now.Sub(st).Seconds())
				if diff > config.MaxSubDurationSec {
					nazalog.Infof("close session. sub session start time=%s, diff=%d", sub.StartTime, diff)
					if sub.Protocol == base.SessionProtocolHlsStr {
						host, _, err := net.SplitHostPort(sub.RemoteAddr)
						if err != nil {
							nazalog.Warnf("split host port failed. remote addr=%s", sub.RemoteAddr)
							continue
						}
						addIpBlacklist(info.ServerId, host, 60)
					} else {
						kickSession(info.ServerId, g.StreamName, sub.SessionId)
					}
				}
			}
		}
	}
}

func logHandler(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	nazalog.Infof("r=%+v, body=%s", r, b)
}

func parseFlag() string {
	cf := flag.String("c", "", "specify conf file")
	flag.Parse()
	return *cf
}

func main() {
	_ = nazalog.Init(func(option *nazalog.Option) {
		option.AssertBehavior = nazalog.AssertFatal
	})
	defer nazalog.Sync()
	base.LogoutStartInfo()

	confFilename := parseFlag()
	rawContent := base.WrapReadConfigFile(confFilename, DefaultConfFilenameList, nil)
	if err := json.Unmarshal(rawContent, &config); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "unmarshal conf file failed. raw content=%s err=%+v", rawContent, err)
		base.OsExitAndWaitPressIfWindows(1)
	}
	nazalog.Infof("config=%+v", config)

	dataManager = datamanager.NewDataManager(datamanager.DmtMemory, config.ServerTimeoutSec)

	nazalog.Infof("> start http server. addr=%s", config.ListenAddr)
	l, err := net.Listen("tcp", config.ListenAddr)
	nazalog.Assert(nil, err)

	m := http.NewServeMux()
	m.HandleFunc("/on_pub_start", OnPubStartHandler)
	m.HandleFunc("/on_pub_stop", OnPubStopHandler)
	m.HandleFunc("/on_sub_start", OnSubStartHandler)
	m.HandleFunc("/on_sub_stop", OnSubStopHandler)
	m.HandleFunc("/on_update", OnUpdateHandler)
	m.HandleFunc("/on_rtmp_connect", logHandler)
	m.HandleFunc("/on_server_start", logHandler)

	srv := http.Server{
		Handler: m,
	}
	err = srv.Serve(l)
	nazalog.Assert(nil, err)
}
