package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/DataDog/datadog-api-client-go/api/v1/datadog"
	"io/ioutil"
	"net/url"
	"strings"
)

// ConfigDDAPI to access the datadog monitor API
type ConfigDDAPI struct {
	APIkey string `json:"apiKeyAuth"`
	APPkey string `json:"appKeyAuth"`
	DDurl  string `json:"dd_url"`
}

func (c *ConfigDDAPI) String() string {
	cc := *c
	if len(cc.APIkey) > 4 {
		cc.APIkey = cc.APIkey[0:4] + "*******"
	}
	if len(cc.APPkey) > 4 {
		cc.APPkey = cc.APPkey[0:4] + "*******"
	}
	return fmt.Sprintf("%#v", cc)
}

const (
	ConfigDDAPIPath = "/etc/config/datadog-client.json"
	appURLPrefix    = "app."
)

// getConfigDDAPI gets all the required env variables
func getConfigDDAPI() (ConfigDDAPI, error) {
	var configDDAPI ConfigDDAPI

	data, err := ioutil.ReadFile(ConfigDDAPIPath)
	if err != nil {
		return configDDAPI, fmt.Errorf("Error while reading configuration: %v", err)
	}
	err = json.Unmarshal(data, &configDDAPI)
	if err != nil {
		return configDDAPI, fmt.Errorf("Error while reading configuration: %v", err)
	}

	var errStr []string
	if configDDAPI.APPkey == "" {
		errStr = append(errStr, "Missing APPKey in configuration")
	}
	if configDDAPI.APIkey == "" {
		errStr = append(errStr, "Missing APIKey in configuration")
	}
	if configDDAPI.DDurl == "" {
		errStr = append(errStr, "Missing DDUrl in configuration")
	}

	if len(errStr) != 0 {
		return configDDAPI, fmt.Errorf(strings.Join(errStr, "; "))
	}
	fmt.Println("DDConfig: " + configDDAPI.String())
	return configDDAPI, nil
}

func getCtx(ctx context.Context, cfg ConfigDDAPI) (context.Context, error) {
	if cfg.DDurl != "" {
		datadogAPIURL, err := url.ParseRequestURI(cfg.DDurl)
		if err != nil {
			return nil, err
		}
		// the configured datadgog url on our clusters may be configured to start with the app prefix
		// however the `site` parameter does not support that (it prepends an api prefix), so trim the app prefix if present
		// see https://docs.google.com/document/d/1dqd7X8iTqMw3e61C7ucfZ22Uf22_oAv8qDxdhbDKb58/edit#heading=h.ipmfddl3vahq for more information
		host := datadogAPIURL.Host
		if strings.HasPrefix(host, appURLPrefix) {
			host = host[len(appURLPrefix):]
		}
		// set both name and site, both are used depending on the server configuration selected internally in the datadog go client
		ctx = context.WithValue(ctx,
			datadog.ContextServerVariables,
			map[string]string{
				"name":     host,
				"site":     host,
				"protocol": datadogAPIURL.Scheme,
			},
		)
	}

	ctx = context.WithValue(
		ctx,
		datadog.ContextAPIKeys,
		map[string]datadog.APIKey{
			"apiKeyAuth": {
				Key: cfg.APIkey,
			},
			"appKeyAuth": {
				Key: cfg.APPkey,
			},
		},
	)
	return ctx, nil
}
