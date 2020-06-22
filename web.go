// Package web is a web dashboard
package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httputil"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/micro/cli"
	"github.com/micro/go-micro"
	"github.com/micro/go-micro/api/server"
	httpapi "github.com/micro/go-micro/api/server/http"
	"github.com/micro/go-micro/client/selector"
	"github.com/micro/go-micro/config/cmd"
	"github.com/micro/go-micro/registry"
	"github.com/micro/go-micro/util/log"
	"github.com/micro/micro/internal/handler"
	"github.com/micro/micro/internal/helper"
	"github.com/micro/micro/internal/stats"
	"github.com/micro/micro/plugin"
	"github.com/serenize/snaker"
)

var (
	re = regexp.MustCompile("^[a-zA-Z0-9]+([a-zA-Z0-9-]*[a-zA-Z0-9]*)?$")
	// Default server name
	Name = "go.micro.web"
	// Default address to bind to
	Address = ":8082"
	// The namespace to serve
	// Example:
	// Namespace + /[Service]/foo/bar
	// Host: Namespace.Service Endpoint: /foo/bar
	Namespace = "go.micro.web"
	// Base path sent to web service.
	// This is stripped from the request path
	// Allows the web service to define absolute paths
	BasePathHeader = "X-Micro-Web-Base-Path"
	statsURL       string
)

type srv struct {
	*mux.Router
}

func (s *srv) proxy() http.Handler {
	sel := selector.NewSelector( // selector为客户端级别的均衡负载
		selector.Registry((*cmd.DefaultOptions().Registry)), // Registry用于实现服务的注册和发现
	)

	director := func(r *http.Request) { // director 接受一个请求作为参数，然后对其进行修改
		kill := func() { // kill() 用于将URL各参数置零
			r.URL.Host = ""
			r.URL.Path = ""
			r.URL.Scheme = ""
			r.Host = ""
			r.RequestURI = ""
		}
		// 要将URL置零的情况
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 2 {
			kill()
			return
		}
		if !re.MatchString(parts[1]) {
			kill()
			return
		}
		next, err := sel.Select(Namespace + "." + parts[1])
		if err != nil {
			kill()
			return
		}

		s, err := next()
		if err != nil {
			kill()
			return
		}
		// director对这个请求设置Header，URL，Host
		r.Header.Set(BasePathHeader, "/"+parts[1])
		r.URL.Host = s.Address
		r.URL.Path = "/" + strings.Join(parts[2:], "/")
		r.URL.Scheme = "http"
		r.Host = r.URL.Host
	}

	return &proxy{
		Default:  &httputil.ReverseProxy{Director: director}, // 发送修改后的请求给后端服务器
		Director: director,
	}
}

func format(v *registry.Value) string {
	// 如果为空，或者Values为0，就反个{}完事
	if v == nil || len(v.Values) == 0 {
		return "{}"
	}
	// 声明一个字符串切片
	var f []string
	// 取出Values，对其进行格式化
	for _, k := range v.Values {
		f = append(f, formatEndpoint(k, 0))
	}
	// 最后加上大括号，每一行Value用,连接，格式化输出
	return fmt.Sprintf("{\n%s\n}", strings.Join(f, ",\n")) // 用逗号连接
}

func formatEndpoint(v *registry.Value, r int) string { // 格式化每一行的Value
	// default format is tabbed plus the value plus new line

	fparts := []string{"", "\"%s\":%s"} // 格式化的格式，加上了""和:
	for i := 0; i < r+1; i++ {
		fparts[0] += "\t"
	}
	// its just a primitive of sorts so return
	// 只有一层
	// 普通的Value就只有名字name和类型type，没有再下一步的Values了，这种可以直接通过DefaultMap映射设置默认值
	if len(v.Values) == 0 {
		return fmt.Sprintf(strings.Join(fparts, ""), snaker.CamelToSnake(v.Name), formatDefault(v, r)) // 给Type做了映射
		//return fmt.Sprintf(strings.Join(fparts, ""), snaker.CamelToSnake(v.Name) + "["+strconv.Itoa(r) + "层name]", formatDefault(v.Type) + "["+strconv.Itoa(r) + "层type]") // 给Type做了映射
		//return fmt.Sprintf(strings.Join(fparts, ""), snaker.CamelToSnake(v.Name), v.Type )
	}

	// this thing has more things, it's complex

	// 还有层在下面
	// 那这个就是里面还有东西的，是嵌套的
	fparts = []string{"", "\"%s\":{ ", "\n"}
	for i := 0; i < r+1; i++ {
		fparts[0] += "\t"
	}
	vals := []interface{}{snaker.CamelToSnake(v.Name)} // 给Type做了映射
	//vals := []interface{}{snaker.CamelToSnake(v.Name) +  "["+strconv.Itoa(r) + "层name]"} // 给Type做了映射
	//vals := []interface{}{snaker.CamelToSnake(v.Name), v.Type}

	for _, val := range v.Values {
		fparts = append(fparts, "%s,\n")
		vals = append(vals, formatEndpoint(val, r+1))
	}

	// at the end
	l := len(fparts) - 1
	fparts[l] = fparts[l][:2] // 去掉最后一个逗号
	fparts = append(fparts, "\n")
	for i := 0; i < r+1; i++ {
		fparts = append(fparts, "\t")
	}
	fparts = append(fparts, "}")
	// 将多层的要放进去DefaultMap 以方便 repeated 的寻找
	repeated := strings.Trim(strings.Trim(fmt.Sprintf(strings.Join(fparts, ""), vals...), "\t"), "\""+v.Name+"\":")
	//repeated := strings.Trim(fmt.Sprintf(strings.Join(fparts, ""), vals...),"\t")
	// 加上前面的换行符
	prefix := []string{""}
	for i := 0; i < r+1; i++ {
		prefix[0] += "\t"
	}
	result := strings.Join(prefix, "") + repeated
	// 加入映射
	DefaultMap[v.Type] = result
	return fmt.Sprintf(strings.Join(fparts, ""), vals...)

}

var DefaultMap = map[string]string{
	"string":    "\"zhouxiaojun\"",
	"bool":      "true",
	"int32":     "32",
	"int64":     "64",
	"uint32":    "32",
	"uint64":    "64",
	"float32":   "32.00",
	"float64":   "64.00",
	"[]string":  "[\"zhou\",\"xiao\",\"jun\"]",
	"[]bool":    "[true,true,true]",
	"[]int32":   "[32,32,32]",
	"[]int64":   "[64,64,64]",
	"[]uint8":   "[8,8,8]",
	"[]uint32":  "[32,32,32]",
	"[]uint64":  "[64,64,64]",
	"[]float32": "[32.00,32.00,32.00]",
	"[]float64": "[64.00,64.00,64.00]",
}

func formatDefault(v *registry.Value, r int) string {

	if DefaultMap[v.Type] != "" {
		return DefaultMap[v.Type]
	}

	// 如果判定是repeated里面的
	if ReName := "[]" + Capitalize(v.Name); ReName == v.Type+"s" || ReName == v.Type+"es" {
		//
		TEMP1 := Capitalize(v.Name[0 : len(v.Name)-1]) // 去掉s
		TEMP2 := Capitalize(v.Name[0 : len(v.Name)-2]) // 去掉es
		post := []string{""}
		for i := 0; i < r+1; i++ {
			post[0] += "\t"
		}
		postfix := strings.Join(post, "")
		if TEMPS := DefaultMap[TEMP1]; TEMPS != "" {
			TEMPS_LIST := []string{TEMPS, TEMPS, TEMPS}                                // 可以控制一个切片里有多少个
			return fmt.Sprintf("[\n%s\n%s]", strings.Join(TEMPS_LIST, ",\n"), postfix) // 用逗号连接
		} else if TEMPES := DefaultMap[TEMP2]; TEMPES != "" {
			TEMPES_LIST := []string{TEMPES, TEMPES, TEMPES}
			return fmt.Sprintf("[\n%s\n%s]", strings.Join(TEMPES_LIST, ",\n"), postfix) // 用逗号连接
		}
	}

	return v.Type
}


func Capitalize(str string) string {
	vv := []rune(str)
	if vv[0] >= 97 && vv[0] <= 122 {
		vv[0] -= 32
	}
	return string(vv)
}

// 各个Handler
func faviconHandler(w http.ResponseWriter, r *http.Request) {
	return
}

func cliHandler(w http.ResponseWriter, r *http.Request) {
	render(w, r, cliTemplate, nil)
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	services, err := (*cmd.DefaultOptions().Registry).ListServices() // 可以得到go.micro.web、go.micro.srv.greeter
	if err != nil {
		http.Error(w, "Error occurred:"+err.Error(), 500)
		return
	}

	var webServices []string
	for _, s := range services {
		if strings.Index(s.Name, Namespace) == 0 && len(strings.TrimPrefix(s.Name, Namespace)) > 0 { // 命名空间是前缀，并且去掉前缀后还有长度
			webServices = append(webServices, strings.Replace(s.Name, Namespace+".", "", 1)) // 去掉命名空间前缀
		}
	}

	sort.Strings(webServices)

	type templateData struct {
		HasWebServices bool
		WebServices    []string
	}

	data := templateData{len(webServices) > 0, webServices}
	render(w, r, indexTemplate, data)
}

func registryHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	svc := r.Form.Get("service")
	// s 是 传递给 serviceTemplate的
	if len(svc) > 0 {
		s, err := (*cmd.DefaultOptions().Registry).GetService(svc) // 可以得到svc具体的request,response,metadata
		if err != nil {
			http.Error(w, "Error occurred:"+err.Error(), 500)
			return
		}

		if len(s) == 0 {
			http.Error(w, "Not found", 404)
			return
		}

		if r.Header.Get("Content-Type") == "application/json" {
			// map 转 json
			b, err := json.Marshal(map[string]interface{}{
				"services": s,
			})
			if err != nil {
				http.Error(w, "Error occurred:"+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
			return
		}

		render(w, r, serviceTemplate, s)
		return
	}

	services, err := (*cmd.DefaultOptions().Registry).ListServices()
	if err != nil {
		http.Error(w, "Error occurred:"+err.Error(), 500)
		return
	}

	sort.Sort(sortedServices{services})

	if r.Header.Get("Content-Type") == "application/json" {
		b, err := json.Marshal(map[string]interface{}{
			"services": services,
		})
		if err != nil {
			http.Error(w, "Error occurred:"+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
		return
	}

	render(w, r, registryTemplate, services)
}

func callHandler(w http.ResponseWriter, r *http.Request) {
	services, err := (*cmd.DefaultOptions().Registry).ListServices()
	if err != nil {
		http.Error(w, "Error occurred:"+err.Error(), 500)
		return
	}

	sort.Sort(sortedServices{services})

	serviceMap := make(map[string][]*registry.Endpoint)
	for _, service := range services {
		// 取每一个服务名下的
		s, err := (*cmd.DefaultOptions().Registry).GetService(service.Name)
		if err != nil {
			continue
		}
		if len(s) == 0 {
			continue
		}
		serviceMap[service.Name] = s[0].Endpoints
	}

	if r.Header.Get("Content-Type") == "application/json" {
		b, err := json.Marshal(map[string]interface{}{
			"services": services,
		})
		if err != nil {
			http.Error(w, "Error occurred:"+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
		return
	}

	render(w, r, callTemplate, serviceMap)
}

func render(w http.ResponseWriter, r *http.Request, tmpl string, data interface{}) {
	t, err := template.New("template").Funcs(template.FuncMap{
		"format": format,
	}).Parse(layoutTemplate)
	if err != nil {
		http.Error(w, "Error occurred:"+err.Error(), 500)
		return
	}
	t, err = t.Parse(tmpl)
	if err != nil {
		http.Error(w, "Error occurred:"+err.Error(), 500)
		return
	}

	if err := t.ExecuteTemplate(w, "layout", map[string]interface{}{
		"StatsURL": statsURL,
		"Results":  data,
	}); err != nil {
		http.Error(w, "Error occurred:"+err.Error(), 500)
	}
}

// 核心逻辑
func run(ctx *cli.Context, srvOpts ...micro.Option) {
	// 首先读取命令参数并将其赋值给全局变量
	if len(ctx.GlobalString("server_name")) > 0 {
		Name = ctx.GlobalString("server_name")
	}
	if len(ctx.String("address")) > 0 {
		Address = ctx.String("address")
	}
	if len(ctx.String("namespace")) > 0 {
		Namespace = ctx.String("namespace")
	}

	// Init plugins
	for _, p := range Plugins() {
		p.Init(ctx)
	}

	var h http.Handler
	r := mux.NewRouter() // 创建一个路由器，r是一个指针类型
	s := &srv{r}
	h = s // h 指向的实际上是 r 的引用

	if ctx.GlobalBool("enable_stats") {
		statsURL = "/stats"
		st := stats.New()
		s.HandleFunc("/stats", st.StatsHandler)
		h = st.ServeHTTP(s)
		st.Start()
		defer st.Stop()
	}
	// 注册处理器
	s.HandleFunc("/client", callHandler)
	s.HandleFunc("/registry", registryHandler)
	s.HandleFunc("/terminal", cliHandler)
	s.HandleFunc("/rpc", handler.RPC)
	s.HandleFunc("/favicon.ico", faviconHandler)
	s.PathPrefix("/{service:[a-zA-Z0-9]+}").Handler(s.proxy())
	s.HandleFunc("/", indexHandler)

	var opts []server.Option
	// 会根据是否设置 enable_acme 或 enable_tls 参数对服务器进行初始化设置，决定是否要启用 HTTPS，以及为哪些服务器启用
	if ctx.GlobalBool("enable_acme") {
		hosts := helper.ACMEHosts(ctx)
		opts = append(opts, server.EnableACME(true))
		opts = append(opts, server.ACMEHosts(hosts...))
	} else if ctx.GlobalBool("enable_tls") {
		config, err := helper.TLSConfig(ctx)
		if err != nil {
			fmt.Println(err.Error())
			return
		}

		opts = append(opts, server.EnableTLS(true))
		opts = append(opts, server.TLSConfig(config))
	}

	// reverse wrap handler
	plugins := append(Plugins(), plugin.Plugins()...)
	for i := len(plugins); i > 0; i-- {
		h = plugins[i-1].Handler()(h)
	}

	srv := httpapi.NewServer(Address)
	srv.Init(opts...)
	srv.Handle("/", h)

	// service opts
	srvOpts = append(srvOpts, micro.Name(Name))
	if i := time.Duration(ctx.GlobalInt("register_ttl")); i > 0 {
		srvOpts = append(srvOpts, micro.RegisterTTL(i*time.Second))
	}
	if i := time.Duration(ctx.GlobalInt("register_interval")); i > 0 {
		srvOpts = append(srvOpts, micro.RegisterInterval(i*time.Second))
	}

	// Initialise Server
	service := micro.NewService(srvOpts...)

	if err := srv.Start(); err != nil {
		log.Fatal(err)
	}

	// Run server
	if err := service.Run(); err != nil {
		log.Fatal(err)
	}

	if err := srv.Stop(); err != nil {
		log.Fatal(err)
	}
}

func Commands(options ...micro.Option) []cli.Command {
	command := cli.Command{
		Name:  "web",
		Usage: "Run the web dashboard",
		Action: func(c *cli.Context) {
			run(c, options...)
		},
		Flags: []cli.Flag{
			cli.StringFlag{
				Name:   "address",
				Usage:  "Set the web UI address e.g 0.0.0.0:8082",
				EnvVar: "MICRO_WEB_ADDRESS",
			},
			cli.StringFlag{
				Name:   "namespace",
				Usage:  "Set the namespace used by the Web proxy e.g. com.example.web",
				EnvVar: "MICRO_WEB_NAMESPACE",
			},
		},
	}

	for _, p := range Plugins() {
		if cmds := p.Commands(); len(cmds) > 0 {
			command.Subcommands = append(command.Subcommands, cmds...)
		}

		if flags := p.Flags(); len(flags) > 0 {
			command.Flags = append(command.Flags, flags...)
		}
	}

	return []cli.Command{command}
}
