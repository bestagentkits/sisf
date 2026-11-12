package web

import "github.com/bestagentkits/sisf/pkg/core"

func coreNext(action string, params map[string]string) core.NextAction {
	return core.NextAction{Action: action, Params: params}
}
