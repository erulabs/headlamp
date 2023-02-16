package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	oidc "github.com/coreos/go-oidc"
	"github.com/gobwas/glob"
	"github.com/google/uuid"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"golang.org/x/oauth2"
)

type HeadlampConfig struct {
	useInCluster     bool
	devMode          bool
	insecure         bool
	kubeConfigPath   string
	port             uint
	staticDir        string
	pluginDir        string
	oidcClientID     string
	oidcClientSecret string
	oidcScopes       []string
	oidcIdpIssuerURL string
	baseURL          string
	// Holds: context-name -> (context, reverse-proxy)
	contextProxies map[string]contextProxy
	proxyURLs      []string
}

const PodAvailabilityCheckTimer = 5 // seconds

const (
	RUNNING = "Running"
	STOPPED = "Stopped"
)

type PortForward struct {
	ID               string `json:"id"`
	closeChan        chan struct{}
	Pod              string `json:"pod"`
	Service          string `json:"service"`
	ServiceNamespace string `json:"serviceNamespace"`
	Namespace        string `json:"namespace"`
	Cluster          string `json:"cluster"`
	Port             string `json:"port"`
	TargetPort       string `json:"targetPort"`
	Status           string `json:"status"`
	Error            string `json:"error"`
}

type PortForwardPayload struct {
	ID               string `json:"id"`
	Namespace        string `json:"namespace"`
	Pod              string `json:"pod"`
	Service          string `json:"service"`
	ServiceNamespace string `json:"serviceNamespace"`
	TargetPort       string `json:"targetPort"`
	Cluster          string `json:"cluster"`
	Port             string `json:"port"`
}

type clientConfig struct {
	Clusters []Cluster `json:"clusters"`
}

type spaHandler struct {
	staticPath string
	indexPath  string
	baseURL    string
}

const (
	KubeConfig = 1 << iota
	DynamicCluster
	InCluster
)

type contextProxy struct {
	context *Context
	proxy   *httputil.ReverseProxy
	source  int // Source indicates if contextProxy is configured from kubeconfig or dynamic cluster or incluster.
}

var pluginListURLs []string

func resetPlugins() {
	pluginListURLs = nil
}

var portForwards = make(map[string][]PortForward)

func portforwardstore(p PortForward) {
	// check if we already have a portforward with the same id if yes update it
	for index, v := range portForwards[p.Cluster] {
		if v.ID == p.ID {
			portForwards[p.Cluster][index] = p
			return
		}
	}

	portForwards[p.Cluster] = append(portForwards[p.Cluster], p)
}

func stopOrDeletePortForward(cluster string, id string, isStopRequest bool) error {
	clusterPortForwards, ok := portForwards[cluster]
	if ok {
		for index, v := range clusterPortForwards {
			if v.ID == id {
				if !isStopRequest {
					portForwards[cluster] = append(clusterPortForwards[:index], clusterPortForwards[index+1:]...)
				} else {
					v.Status = STOPPED
					v.closeChan <- struct{}{}
					clusterPortForwards[index] = v
				}

				return nil
			}
		}
	}

	return fmt.Errorf("PortForward not found")
}

func getPortForwardList(cluster string) []PortForward {
	return portForwards[cluster]
}

func getPortForwardByID(cluster string, id string) PortForward {
	val, ok := portForwards[cluster]
	if ok {
		for _, v := range val {
			if v.ID == id {
				return v
			}
		}
	}

	return PortForward{}
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// get the absolute path to prevent directory traversal
	path, err := filepath.Abs(r.URL.Path)
	if err != nil {
		// if we failed to get the absolute path respond with a 400 bad request
		// and stop
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	path = strings.TrimPrefix(path, h.baseURL)

	// prepend the path with the path to the static directory
	path = filepath.Join(h.staticPath, path)

	// check whether a file exists at the given path
	_, err = os.Stat(path)
	if os.IsNotExist(err) {
		// file does not exist, serve index.html
		http.ServeFile(w, r, filepath.Join(h.staticPath, h.indexPath))
		return
	} else if err != nil {
		// if we got an error (that wasn't that the file doesn't exist) stating the
		// file, return a 500 internal server error and stop
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// The file does exist, so we serve that.
	http.ServeFile(w, r, path)
}

// returns True if a file exists.
func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}

	return !info.IsDir()
}

// copy a file, whilst doing some search/replace on the data.
func copyReplace(src string, dst string,
	search []byte, replace []byte,
	search2 []byte, replace2 []byte,
) {
	data, err := ioutil.ReadFile(src)
	if err != nil {
		log.Fatal(err)
	}

	data1 := bytes.ReplaceAll(data, search, replace)
	data2 := bytes.ReplaceAll(data1, search2, replace2)
	fileMode := 0600

	err = ioutil.WriteFile(dst, data2, fs.FileMode(fileMode))
	if err != nil {
		log.Fatal(err)
	}
}

func (p PortForwardPayload) validatePortForward() error {
	if p.Namespace == "" {
		return fmt.Errorf("namespace is required")
	}

	if p.Pod == "" {
		return fmt.Errorf("pod name is required")
	}

	if p.TargetPort == "" {
		return fmt.Errorf("targetPort is required")
	}

	if p.Cluster == "" {
		return fmt.Errorf("cluster name is required")
	}

	return nil
}

// make sure the base-url is updated in the index.html file.
func baseURLReplace(staticDir string, baseURL string) {
	indexBaseURL := path.Join(staticDir, "index.baseUrl.html")
	index := path.Join(staticDir, "index.html")

	replaceURL := baseURL
	if baseURL == "" {
		// We have to do the replace when baseURL == "" because of the case when
		//   someone first does a different baseURL. If we didn't it would stay stuck
		//   on that previous baseURL.
		replaceURL = "/"
	}

	if !fileExists(indexBaseURL) {
		copyReplace(index, indexBaseURL, []byte(""), []byte(""), []byte(""), []byte(""))
	}

	copyReplace(indexBaseURL,
		index,
		[]byte("./"),
		[]byte(baseURL+"/"),
		[]byte("headlampBaseUrl=\".\""),
		[]byte("headlampBaseUrl=\""+replaceURL+"\""))
}

func getOidcCallbackURL(r *http.Request, config *HeadlampConfig) string {
	urlScheme := r.URL.Scheme
	if urlScheme == "" {
		// check proxy headers first
		fwdProto := r.Header.Get("X-Forwarded-Proto")

		switch {
		case fwdProto != "":
			urlScheme = fwdProto
		case strings.HasPrefix(r.Host, "localhost:") || r.TLS == nil:
			urlScheme = "http"
		default:
			urlScheme = "https"
		}
	}

	// Clean up + add the base URL to the redirect URL
	hostWithBaseURL := strings.Trim(r.Host, "/")
	baseURL := strings.Trim(config.baseURL, "/")

	if baseURL != "" {
		hostWithBaseURL = hostWithBaseURL + "/" + baseURL
	}

	return fmt.Sprintf("%s://%s/oidc-callback", urlScheme, hostWithBaseURL)
}

func serveWithNoCacheHeader(fs http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Cache-Control", "no-cache")
		fs.ServeHTTP(w, r)
	}
}

//nolint:gocognit,funlen,gocyclo
func createHeadlampHandler(config *HeadlampConfig) http.Handler {
	kubeConfigPath := config.kubeConfigPath

	log.Printf("plugins-dir: %s\n", config.pluginDir)

	if !config.useInCluster {
		// in-cluster mode is unlikely to want reloading plugins.
		go watchForChanges(config.pluginDir)
		// in-cluster mode is unlikely to want reloading kubeconfig.
		go watchForKubeConfigChanges(config)
	}

	var contexts []Context

	config.contextProxies = make(map[string]contextProxy)

	// In-cluster
	if config.useInCluster {
		context, err := GetOwnContext(config)
		if err != nil {
			log.Println("Failed to get in-cluster config", err)
		}

		proxy, err := config.createProxyForContext(*context)
		if err != nil {
			log.Printf("Error setting up proxy for context %s: %s\n", context.Name, err)
		}

		config.contextProxies[context.Name] = contextProxy{
			context,
			proxy,
			InCluster,
		}
	}

	// KubeConfig clusters
	contexts = append(contexts, getContextFromKubeConfigs(kubeConfigPath)...)

	if config.staticDir != "" {
		baseURLReplace(config.staticDir, config.baseURL)
	}

	// For when using a base-url, like "/headlamp" with a reverse proxy.
	var r *mux.Router
	if config.baseURL == "" {
		r = mux.NewRouter()
	} else {
		baseRoute := mux.NewRouter()
		r = baseRoute.PathPrefix(config.baseURL).Subrouter()
	}

	fmt.Println("*** Headlamp Server ***")
	fmt.Println("  API Routers:")

	if len(contexts) == 0 {
		log.Println("No contexts/clusters configured by default!")
	} else {
		for i := range contexts {
			context := &contexts[i]
			proxy, err := config.createProxyForContext(*context)
			if err != nil {
				log.Printf("Error setting up proxy for context %s: %s\n", context.Name, err)
				continue
			}

			fmt.Printf("\tlocalhost:%d%s%s/{api...} -> %s\n", config.port, config.baseURL, "/clusters/"+context.Name,
				*context.cluster.getServer())

			config.contextProxies[context.Name] = contextProxy{
				context,
				proxy,
				KubeConfig,
			}
		}
	}

	addPluginRoutes(config, r)

	config.handleClusterRequests(r)

	r.HandleFunc("/externalproxy", func(w http.ResponseWriter, r *http.Request) {
		url, err := url.Parse(r.Header.Get("proxy-to"))
		if err != nil {
			log.Fatal("Failed to get URL from server", err)
		}
		isURLContainedInProxyURLs := false
		for _, proxyURL := range config.proxyURLs {
			g := glob.MustCompile(proxyURL)
			if g.Match(url.String()) {
				isURLContainedInProxyURLs = true
				break
			}
		}
		if !isURLContainedInProxyURLs {
			http.Error(w, "no allowed proxy url match, request denied ", http.StatusBadRequest)
		}
		proxy := httputil.NewSingleHostReverseProxy(url)
		r.Host = url.Host
		r.URL.Host = url.Host
		r.URL.Scheme = url.Scheme
		r.RequestURI = url.RequestURI()

		log.Println("Requesting ", r.URL.String())
		proxy.ServeHTTP(w, r)
	})

	// Configuration
	r.HandleFunc("/config", config.getConfig).Methods("GET")

	config.addClusterSetupRoute(r)

	oauthRequestMap := make(map[string]*OauthConfig)

	r.HandleFunc("/oidc", func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		cluster := r.URL.Query().Get("cluster")
		if config.insecure {
			tr := &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			}
			insecureClient := &http.Client{Transport: tr}
			ctx = oidc.ClientContext(ctx, insecureClient)
		}

		oidcAuthConfig, err := GetClusterOidcConfig(cluster)
		if err != nil {
			log.Printf("Error getting %s cluster oidc config %s", cluster, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		provider, err := oidc.NewProvider(ctx, oidcAuthConfig.IdpIssuerURL)
		if err != nil {
			log.Printf("Error while fetching the provider from %s error %s", oidcAuthConfig.IdpIssuerURL, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		oidcConfig := &oidc.Config{
			ClientID: oidcAuthConfig.ClientID,
		}

		verifier := provider.Verifier(oidcConfig)
		oauthConfig := &oauth2.Config{
			ClientID:     oidcAuthConfig.ClientID,
			ClientSecret: oidcAuthConfig.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  getOidcCallbackURL(r, config),
			Scopes:       append([]string{oidc.ScopeOpenID}, oidcAuthConfig.Scopes...),
		}
		/* we encode the cluster to base64 and set it as state so that when getting redirected
		by oidc we can use this state value to get cluster name
		*/
		state := base64.StdEncoding.EncodeToString([]byte(cluster))
		oauthRequestMap[state] = &OauthConfig{Config: oauthConfig, Verifier: verifier, Ctx: ctx}
		http.Redirect(w, r, oauthConfig.AuthCodeURL(state), http.StatusFound)
	}).Queries("cluster", "{cluster}")

	r.HandleFunc("/portforward", func(w http.ResponseWriter, r *http.Request) {
		var p PortForwardPayload
		err := json.NewDecoder(r.Body).Decode(&p)
		if err != nil {
			http.Error(w, "invalid request "+err.Error(), http.StatusBadRequest)
			return
		}
		if p.ID == "" {
			id := uuid.New().String()
			p.ID = id
		}

		reqToken := r.Header.Get("Authorization")
		splitToken := strings.Split(reqToken, "Bearer ")
		var token string
		if reqToken != "" || len(splitToken) > 2 {
			token = splitToken[1]
		}

		err = p.validatePortForward()
		if err != nil {
			http.Error(w, "invalid request "+err.Error(), http.StatusBadRequest)
			return
		}
		if p.Port == "" {
			// if no port is specified find a available port
			freePort, err := GetFreePort()
			if err != nil {
				http.Error(w, "can't find any available port "+err.Error(), http.StatusInternalServerError)
			}
			if freePort != 0 {
				p.Port = strconv.Itoa(freePort)
			}
		}

		if err != nil {
			http.Error(w, "failed to marshal port forward payload "+err.Error(), http.StatusInternalServerError)
			return
		}

		err = config.startPortForward(p, token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		jsonPayload, err := json.Marshal(p)
		if err != nil {
			log.Printf("Error decoding portforward payload %s", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if _, err = w.Write(jsonPayload); err != nil {
			http.Error(w, "failed to write json payload to response write "+err.Error(), http.StatusInternalServerError)
		}
	}).Methods("POST")

	r.HandleFunc("/portforward", func(w http.ResponseWriter, r *http.Request) {
		type deletePayload struct {
			ID           string `json:"id"`
			Cluster      string `json:"cluster"`
			StopOrDelete bool   `json:"stopOrDelete"`
		}
		var dp deletePayload
		err := json.NewDecoder(r.Body).Decode(&dp)
		if err != nil {
			log.Printf("Error decoding delete portforward payload %s", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if dp.ID == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		if dp.Cluster == "" {
			http.Error(w, "cluster is required", http.StatusBadRequest)
			return
		}
		err = stopOrDeletePortForward(dp.Cluster, dp.ID, dp.StopOrDelete)
		if err == nil {
			if _, err := w.Write([]byte("stopped")); err != nil {
				http.Error(w, "failed to write response "+err.Error(), http.StatusInternalServerError)
			}
			return
		}
		http.Error(w, "failed to delete port forward "+err.Error(), http.StatusInternalServerError)
	}).Methods("DELETE")

	r.HandleFunc("/portforward/list", func(w http.ResponseWriter, r *http.Request) {
		cluster := r.URL.Query().Get("cluster")
		if cluster == "" {
			http.Error(w, "cluster is required", http.StatusBadRequest)
			return
		}
		ports := getPortForwardList(cluster)

		jsonPayload, err := json.Marshal(ports)
		if err != nil {
			http.Error(w, "failed to marshal port forward list "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err = w.Write(jsonPayload); err != nil {
			http.Error(w, "failed to write json payload to response "+err.Error(), http.StatusInternalServerError)
		}
	})

	r.HandleFunc("/portforward", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		cluster := r.URL.Query().Get("cluster")
		if cluster == "" {
			http.Error(w, "cluster is required", http.StatusBadRequest)
			return
		}
		if id == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		p := getPortForwardByID(cluster, id)
		if p.ID == "" {
			http.Error(w, "no portforward running with id "+id, http.StatusNotFound)
			return
		}
		type payload struct {
			ID        string `json:"id"`
			Pod       string `json:"pod"`
			Service   string `json:"service"`
			Cluster   string `json:"cluster"`
			Namespace string `json:"namespace"`
		}
		portForwardStruct := payload{
			ID:        p.ID,
			Pod:       p.Pod,
			Namespace: p.Namespace,
			Cluster:   p.Cluster,
			Service:   p.Service,
		}
		jsonPayload, err := json.Marshal(portForwardStruct)
		if err != nil {
			http.Error(w, "failed to marshal port forward get "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(jsonPayload); err != nil {
			http.Error(w, "failed to write json payload "+err.Error(), http.StatusInternalServerError)
		}
	}).Methods("GET")

	r.HandleFunc("/oidc-callback", func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		decodedState, err := base64.StdEncoding.DecodeString(state)
		if err != nil {
			http.Error(w, "wrong state set, invalid request "+err.Error(), http.StatusBadRequest)
		}
		if state == "" {
			http.Error(w, "invalid request state is empty", http.StatusBadRequest)
			return
		}
		//nolint:nestif
		if oauthConfig, ok := oauthRequestMap[state]; ok {
			oauth2Token, err := oauthConfig.Config.Exchange(oauthConfig.Ctx, r.URL.Query().Get("code"))
			if err != nil {
				http.Error(w, "Failed to exchange token: "+err.Error(), http.StatusInternalServerError)
				return
			}

			rawIDToken, ok := oauth2Token.Extra("id_token").(string)
			if !ok {
				http.Error(w, "No id_token field in oauth2 token.", http.StatusInternalServerError)
				return
			}

			idToken, err := oauthConfig.Verifier.Verify(oauthConfig.Ctx, rawIDToken)
			if err != nil {
				http.Error(w, "Failed to verify ID Token: "+err.Error(), http.StatusInternalServerError)
				return
			}
			resp := struct {
				OAuth2Token   *oauth2.Token
				IDTokenClaims *json.RawMessage // ID Token payload is just JSON.
			}{oauth2Token, new(json.RawMessage)}

			if err := idToken.Claims(&resp.IDTokenClaims); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			var redirectURL string
			if config.devMode {
				redirectURL = "http://localhost:3000/"
			} else {
				redirectURL = "/"
			}

			redirectURL += fmt.Sprintf("auth?cluster=%1s&token=%2s", decodedState, rawIDToken)
			http.Redirect(w, r, redirectURL, http.StatusSeeOther)
		} else {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
	})

	// Serve the frontend if needed
	if config.staticDir != "" {
		spa := spaHandler{staticPath: config.staticDir, indexPath: "index.html", baseURL: config.baseURL}
		r.PathPrefix("/").Handler(spa)

		http.Handle("/", r)
	}

	// On dev mode we're loose about where connections come from
	if config.devMode {
		headers := handlers.AllowedHeaders([]string{"X-Requested-With", "Content-Type", "Authorization"})
		methods := handlers.AllowedMethods([]string{"GET", "POST", "PUT", "HEAD", "DELETE", "PATCH", "OPTIONS"})
		origins := handlers.AllowedOrigins([]string{"*"})

		return handlers.CORS(headers, methods, origins)(r)
	}

	return r
}

func StartHeadlampServer(config *HeadlampConfig) {
	handler := createHeadlampHandler(config)

	// Start server
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", config.port), handler)) //nolint:gosec
}

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

//nolint:funlen
func (c *HeadlampConfig) startPortForward(p PortForwardPayload, token string) error {
	ports := []string{fmt.Sprintf(p.Port + ":" + p.TargetPort)}
	ctxtProxy, ok := c.contextProxies[p.Cluster]

	if !ok {
		return fmt.Errorf("cluster %s not found", p.Cluster)
	}

	var caData []byte

	var err error

	if caData, err = ctxtProxy.context.cluster.getCAData(); err != nil {
		return fmt.Errorf("failed to get CA data: %v", err)
	}

	rConf := &rest.Config{
		Host:        ctxtProxy.context.cluster.config.Server,
		BearerToken: token,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: caData,
		},
	}

	if ctxtProxy.context.authInfo != nil {
		if ctxtProxy.context.authInfo.ClientKey != "" {
			rConf.TLSClientConfig.KeyFile = ctxtProxy.context.authInfo.ClientKey
		}

		if ctxtProxy.context.authInfo.ClientCertificate != "" {
			rConf.TLSClientConfig.CertFile = ctxtProxy.context.authInfo.ClientCertificate
		}

		if ctxtProxy.context.authInfo.ClientKeyData != nil {
			rConf.TLSClientConfig.KeyData = ctxtProxy.context.authInfo.ClientKeyData
		}

		if ctxtProxy.context.authInfo.ClientCertificateData != nil {
			rConf.TLSClientConfig.CertData = ctxtProxy.context.authInfo.ClientCertificateData
		}
	}

	clientset, err := kubernetes.NewForConfig(rConf)
	if err != nil {
		return fmt.Errorf("failed to create portforward request: %v", err)
	}

	roundTripper, upgrader, err := spdy.RoundTripperFor(rConf)
	if err != nil {
		log.Printf("Error: failed to create round tripper: %s", err)
		return fmt.Errorf("failed to create portforward request")
	}

	requestURL := fmt.Sprintf("%s/api/v1/namespaces/%s/pods/%s/portforward", rConf.Host, p.Namespace, p.Pod)

	reqURL, err := url.Parse(requestURL)
	if err != nil {
		return fmt.Errorf("portforward request: failed to parse url: %v", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, reqURL)
	stopChan, readyChan := make(chan struct{}), make(chan struct{}, 1)
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)

	forwarder, err := portforward.New(dialer, ports, stopChan, readyChan, out, errOut)
	if err != nil {
		return fmt.Errorf("portforward request: failed to create portforward: %v", err)
	}

	portForwardToStore := PortForward{
		ID:               p.ID,
		closeChan:        stopChan,
		Pod:              p.Pod,
		Cluster:          p.Cluster,
		Namespace:        p.Namespace,
		Service:          p.Service,
		ServiceNamespace: p.ServiceNamespace,
		TargetPort:       p.TargetPort,
		Status:           RUNNING,
		Port:             p.Port,
		Error:            "",
	}

	go func() {
		if err = forwarder.ForwardPorts(); err != nil { // Locks until stopChan is closed.
			log.Printf("Error: failed to forward ports: %s", err)
			stopChan <- struct{}{}

			portForwardToStore.Error = err.Error()
			portforwardstore(portForwardToStore)
		}
	}()

	for {
		<-readyChan
		break
	}

	if errOut.String() == "" {
		portforwardstore(portForwardToStore)
	}

	/* check every PodAvailabilityCheckTimer seconds if the pod for which we started a portforward is running
	if not then we close the channel
	*/
	ticker := time.NewTicker(PodAvailabilityCheckTimer * time.Second)

	go func() {
		for range ticker.C {
			ctx := context.Background()

			pod, err := clientset.CoreV1().Pods(p.Namespace).Get(ctx, p.Pod, v1.GetOptions{})
			if errors.Is(err, syscall.ECONNREFUSED) {
				continue
			} else if err != nil {
				log.Printf("portforward: failed to get pod: %s", err)
				stopChan <- struct{}{}
				portForwardToStore.Error = err.Error()
				portforwardstore(portForwardToStore)
				ticker.Stop()
				break
			}

			if pod.Status.Phase != corev1.PodRunning {
				// close the channel if this pod is not running
				stopChan <- struct{}{}

				portForwardToStore.Error = "Pod is not running"
				portforwardstore(portForwardToStore)
				ticker.Stop()

				break
			}
		}
	}()

	return nil
}

func (c *HeadlampConfig) handleClusterRequests(router *mux.Router) {
	router.PathPrefix("/clusters/{clusterName}/{api:.*}").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clusterName := mux.Vars(r)["clusterName"]
		ctxtProxy, ok := c.contextProxies[clusterName]
		if !ok {
			http.NotFound(w, r)
			return
		}

		server, err := url.Parse(*ctxtProxy.context.cluster.getServer())
		if err != nil {
			log.Printf("Error: failed to get valid URL from server %s: %s", server, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Try to add the Authorization header if it's not already set from the client.
		if r.Header.Get("Authorization") == "" {
			token := c.contextProxies[clusterName].context.authInfo.Token
			if token != "" {
				r.Header.Add("Authorization", "Bearer "+token)
			}
		}

		handler := proxyHandler(server, ctxtProxy.proxy)
		handler(w, r)
	})
}

func (c *HeadlampConfig) getClusters() []Cluster {
	clusters := make([]Cluster, 0, len(c.contextProxies))

	for _, contextProxy := range c.contextProxies {
		context := contextProxy.context
		clusters = append(clusters, *context.getCluster())
	}

	return clusters
}

func getTransportProxy(cluster *Cluster) func(*http.Request) (*url.URL, error) {
	var transportProxy func(*http.Request) (*url.URL, error)

	if cluster.ProxyURL != "" {
		proxyURL, _ := url.Parse(cluster.ProxyURL)
		transportProxy = http.ProxyURL(proxyURL)
	}

	return transportProxy
}

func (c *HeadlampConfig) createProxyForContext(context Context) (*httputil.ReverseProxy, error) {
	cluster := context.getCluster()
	name := cluster.getName()

	server, err := url.Parse(*cluster.getServer())
	if err != nil {
		return nil, fmt.Errorf("failed to get URL from server %s: %w", *name, err)
	}

	// Create a reverse proxy to direct the API calls to the right server
	proxy := httputil.NewSingleHostReverseProxy(server)

	// Set up certificates for TLS
	rootCAs := x509.NewCertPool()

	shouldVerifyTLS := !c.insecure || cluster.shouldVerifyTLS()
	if shouldVerifyTLS {
		certificate, err := cluster.getCAData()
		if err != nil {
			return nil, err
		}

		rootCAs.AppendCertsFromPEM(certificate)
	}

	var certs []tls.Certificate

	// We allow the use of client certificates now, so let's try to load them
	// if they exist.
	clientCert := context.getClientCertificate()
	if clientCert != "" {
		clientKey := context.getClientKey()
		if clientKey == "" {
			return nil, fmt.Errorf("found a ClientCertificate entry, but not a ClientKey")
		} else if cert, err := tls.LoadX509KeyPair(clientCert, clientKey); err == nil {
			certs = append(certs, cert)
		}
	}

	clientCertData := context.getClientCertificateData()
	if clientCertData != nil {
		clientKeyData := context.getClientKeyData()
		if clientKeyData == nil {
			return nil, fmt.Errorf("found a ClientCertificateData entry, but not a ClientKeyData")
		} else if cert, err := tls.X509KeyPair(clientCertData, clientKeyData); err == nil {
			certs = append(certs, cert)
		}
	}

	tls := &tls.Config{
		InsecureSkipVerify: shouldVerifyTLS, //nolint:gosec
		RootCAs:            rootCAs,
		Certificates:       certs,
	}

	proxy.Transport = &http.Transport{
		Proxy:           getTransportProxy(cluster),
		TLSClientConfig: tls,
	}

	return proxy, nil
}

func setPluginReloadHeader(writer http.ResponseWriter) {
	// We signal back to the frontend through a header.
	// See apiProxy.ts in the frontend for how it handles this.
	log.Println("Sending reload plugins signal to frontend")

	// Allow JavaScript access to X-Reload header. Because denied by default.
	writer.Header().Set("Access-Control-Expose-Headers", "X-Reload")
	writer.Header().Set("X-Reload", "reload")
}

func proxyHandler(url *url.URL, proxy *httputil.ReverseProxy) func(http.ResponseWriter, *http.Request) {
	return func(writer http.ResponseWriter, request *http.Request) {
		request.Host = url.Host
		request.Header.Set("X-Forwarded-Host", request.Header.Get("Host"))
		request.URL.Host = url.Host
		request.URL.Path = mux.Vars(request)["api"]
		request.URL.Scheme = url.Scheme

		if pluginsChanged() {
			resetPlugins()
			setPluginReloadHeader(writer)
		}

		log.Println("Requesting ", request.URL.String())
		proxy.ServeHTTP(writer, request)
	}
}

func (c *HeadlampConfig) getConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	clientConfig := clientConfig{c.getClusters()}

	if err := json.NewEncoder(w).Encode(&clientConfig); err != nil {
		log.Println("Error encoding config", err)
	}
}

func (c *HeadlampConfig) addCluster(w http.ResponseWriter, r *http.Request) {
	clusterReq := ClusterReq{}
	if err := json.NewDecoder(r.Body).Decode(&clusterReq); err != nil {
		fmt.Println(err)
		http.Error(w, "Error decoding cluster info", http.StatusBadRequest)

		return
	}

	if clusterReq.Name == "" || clusterReq.Server == "" {
		http.Error(w, "Error creating cluster with invalid info; please provide a 'name' and 'server' fields at least.",
			http.StatusBadRequest)
		return
	}

	context := Context{
		Name: clusterReq.Name,
		cluster: Cluster{
			Name:   clusterReq.Name,
			Server: clusterReq.Server,
			config: &clientcmdapi.Cluster{
				Server:                   clusterReq.Server,
				InsecureSkipTLSVerify:    clusterReq.InsecureSkipTLSVerify,
				CertificateAuthorityData: clusterReq.CertificateAuthorityData,
			},
			Metadata: clusterReq.Metadata,
		},
	}

	proxy, err := c.createProxyForContext(context)
	if err != nil {
		log.Printf("Error creating proxy for cluster %s: %s", clusterReq.Name, err)
		http.Error(w, "Error setting up cluster", http.StatusBadRequest)

		return
	}

	_, isReplacement := c.contextProxies[clusterReq.Name]

	c.contextProxies[clusterReq.Name] = contextProxy{
		&context,
		proxy,
		DynamicCluster,
	}

	if isReplacement {
		fmt.Printf("Replaced cluster \"%s\" proxy by:\n", context.Name)
	} else {
		fmt.Println("Created new cluster proxy:")
	}

	fmt.Printf("\tlocalhost:%d%s%s/{api...} -> %s\n", c.port, c.baseURL, "/clusters/"+context.Name, clusterReq.Server)

	w.WriteHeader(http.StatusCreated)
	c.getConfig(w, r)
}

func (c *HeadlampConfig) deleteCluster(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if _, ok := c.contextProxies[name]; !ok {
		http.Error(w, "Cluster not found", http.StatusNotFound)
		return
	}

	if c.contextProxies[name].source != DynamicCluster {
		http.Error(w, "Cannot delete a static cluster", http.StatusForbidden)
		return
	}

	delete(c.contextProxies, name)
	fmt.Printf("Removed cluster \"%s\" proxy\n", name)

	c.getConfig(w, r)
}

func (c *HeadlampConfig) addClusterSetupRoute(r *mux.Router) {
	// We do not support this feature when in-cluster
	if c.useInCluster {
		return
	}

	r.HandleFunc("/cluster", c.addCluster).Methods("POST")

	// Delete a cluster
	r.HandleFunc("/cluster/{name}", c.deleteCluster).Methods("DELETE")
}

func absPath(path string) (string, error) {
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}

	currentUser, err := user.Current()
	if err != nil {
		return "", err
	}

	return filepath.Join(currentUser.HomeDir, path[2:]), nil
}
