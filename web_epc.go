package web

import (
	"encoding/json"
	"fmt"
	"git.code.oa.com/cloud_industry/epc/epc-cli/command"
	"github.com/gorilla/mux"
	"github.com/micro/go-micro"
	"github.com/micro/go-micro/api/server"
	httpapi "github.com/micro/go-micro/api/server/http"
	"github.com/micro/go-micro/client/selector"
	"github.com/micro/go-micro/registry"
	"github.com/micro/go-micro/service/grpc"
	"github.com/serenize/snaker"
	"github.com/spf13/cobra"
	"html/template"
	"net/http"
	"net/http/httputil"
	"regexp"
	"sort"
	"strings"
)

func init() {
	command.RootCmd.AddCommand(webCmd)
}

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "epc web",
	Long:  `页面支持调试 gprc`,
	RunE:  web,
}

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

	service micro.Service
)

type srv struct {
	*mux.Router
}

func (s *srv) proxy() http.Handler {
	sel := selector.NewSelector(
		selector.Registry(service.Client().Options().Registry),
	)

	director := func(r *http.Request) {
		kill := func() {
			r.URL.Host = ""
			r.URL.Path = ""
			r.URL.Scheme = ""
			r.Host = ""
			r.RequestURI = ""
		}

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

		r.Header.Set(BasePathHeader, "/"+parts[1])
		r.URL.Host = s.Address
		r.URL.Path = "/" + strings.Join(parts[2:], "/")
		r.URL.Scheme = "http"
		r.Host = r.URL.Host
	}

	return &proxy{
		Default:  &httputil.ReverseProxy{Director: director},
		Director: director,
	}
}

func faviconHandler(w http.ResponseWriter, r *http.Request) {
	return
}

func cliHandler(w http.ResponseWriter, r *http.Request) {
	render(w, r, cliTemplate, nil)
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	services, err := (service.Client().Options().Registry).ListServices()
	if err != nil {
		http.Error(w, "Error occurred:"+err.Error(), 500)
		return
	}

	var webServices []string
	for _, s := range services {
		if strings.Index(s.Name, Namespace) == 0 && len(strings.TrimPrefix(s.Name, Namespace)) > 0 {
			webServices = append(webServices, strings.Replace(s.Name, Namespace+".", "", 1))
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "ParseForm err:"+err.Error(), http.StatusBadRequest)
		return
	}
	svc := r.Form.Get("service")

	if len(svc) > 0 {
		s, err := (service.Client().Options().Registry).GetService(svc)
		if err != nil {
			http.Error(w, "Error occurred:"+err.Error(), http.StatusInternalServerError)
			return
		}

		if len(s) == 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		if r.Header.Get("Content-Type") == "application/json" {
			b, err := json.Marshal(map[string]interface{}{
				"services": s,
			})
			if err != nil {
				http.Error(w, "Error occurred:"+err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
			return
		}

		render(w, r, serviceTemplate, s)
		return
	}

	services, err := (service.Client().Options().Registry).ListServices()
	if err != nil {
		http.Error(w, "Error occurred:"+err.Error(), http.StatusInternalServerError)
		return
	}

	sort.Sort(sortedServices{services})

	if r.Header.Get("Content-Type") == "application/json" {
		b, err := json.Marshal(map[string]interface{}{
			"services": services,
		})
		if err != nil {
			http.Error(w, "Error occurred:"+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
		return
	}

	render(w, r, registryTemplate, services)
}

func callHandler(w http.ResponseWriter, r *http.Request) {
	client := service.Client()
	services, err := (client.Options().Registry).ListServices()
	if err != nil {
		http.Error(w, "Error occurred:"+err.Error(), http.StatusInternalServerError)
		return
	}

	sort.Sort(sortedServices{services})

	serviceMap := make(map[string][]*registry.Endpoint)
	for _, service := range services {
		s, err := (client.Options().Registry).GetService(service.Name)
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
			http.Error(w, "Error occurred:"+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
		return
	}

	render(w, r, callTemplate, serviceMap)
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
	return fmt.Sprintf("{\n%s}", strings.Join(f, ",")) // 用逗号连接
}

func formatEndpoint(v *registry.Value, r int) string { // 格式化每一行的Value
	fparts := []string{"", "\"%s\":%s", "\n"} // 格式化的格式，加上了""和:
	for i := 0; i < r+1; i++ {
		fparts[0] += "\t"
	}
	// 只有一层
	// 普通的Value就只有名字name和类型type，没有再下一步的Values了，这种可以直接通过DefaultMap映射设置默认值
	if len(v.Values) == 0 {
		return fmt.Sprintf(strings.Join(fparts, ""), snaker.CamelToSnake(v.Name), formatDefault(v, r))
	}

	// 那这个就是里面还有东西的，是嵌套的
	fparts = []string{"", "\"%s\":{ ", "\n"}
	for i := 0; i < r+1; i++ {
		fparts[0] += "\t"
	}
	vals := []interface{}{snaker.CamelToSnake(v.Name)}
	for _, val := range v.Values {
		fparts = append(fparts, "%s,")
		vals = append(vals, formatEndpoint(val, r+1))
	}

	// 末尾的格式
	l := len(fparts) - 1
	fparts[l] = fparts[l][:2] // 去掉最后一个逗号
	for i := 0; i < r+1; i++ {
		fparts[l] += "\t"
	}
	fparts = append(fparts, "}\n")
	// 将多层的要放进去DefaultMap 以方便 repeated 的寻找
	repeated := strings.Trim(strings.Trim(fmt.Sprintf(strings.Join(fparts, ""), vals...),"\t"), "\"" + v.Name + "\":")
	// 加上上面为了去掉"v.Name"所去掉的的换行符
	prefix := []string{""}
	for i := 0; i < r+1; i++ {
		prefix[0] += "\t"
	}
	result := strings.Join(prefix,"") + repeated
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

func formatDefault(v *registry.Value,r int) string {

	if DefaultMap[v.Type] != "" {
		return DefaultMap[v.Type]
	}

	// 如果判定是repeated里面的
	if ReName := "[]" + Capitalize(v.Name); ReName == v.Type+"s" || ReName == v.Type+"es" {
		//
		TEMP1 := Capitalize(v.Name[0:len(v.Name)-1]) // 去掉s
		TEMP2 := Capitalize(v.Name[0:len(v.Name)-2]) // 去掉es
		post := []string{""}
		for i := 0; i < r+1; i++ {
			post[0] += "\t"
		}
		postfix := strings.Join(post,"")
		if TEMPS := DefaultMap[TEMP1] ; TEMPS != "" {
			TEMPS_LIST := []string{TEMPS,TEMPS,TEMPS} // 可以控制一个切片里有多少个
			return  fmt.Sprintf("[\n%s%s]", strings.Join(TEMPS_LIST, ","), postfix) // 用逗号连接
		} else if TEMPES := DefaultMap[TEMP2] ; TEMPES != "" {
			TEMPES_LIST := []string{TEMPES,TEMPES,TEMPES}
			return  fmt.Sprintf("[\n%s%s]", strings.Join(TEMPES_LIST, ","), postfix) // 用逗号连接
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

func web(cmd *cobra.Command, args []string) error {
	// Initialise Server
	srvOpts := make([]micro.Option, 0)
	srvOpts = append(srvOpts, micro.Name(Name))
	service = grpc.NewService(srvOpts...)
	service.Init()

	// Init HTTP Server
	var h http.Handler
	r := mux.NewRouter()
	s := &srv{r}
	h = s

	s.HandleFunc("/client", callHandler)
	s.HandleFunc("/registry", registryHandler)
	s.HandleFunc("/terminal", cliHandler)
	s.HandleFunc("/rpc", rpc)
	s.HandleFunc("/favicon.ico", faviconHandler)
	s.PathPrefix("/{service:[a-zA-Z0-9]+}").Handler(s.proxy())
	s.HandleFunc("/", indexHandler)

	var opts []server.Option

	srv := httpapi.NewServer(Address)
	if err := srv.Init(opts...); err != nil {
		return err
	}
	srv.Handle("/", h)

	if err := srv.Start(); err != nil {
		return err
	}

	// Run server
	if err := service.Run(); err != nil {
		return err
	}

	if err := srv.Stop(); err != nil {
		return err
	}

	return nil
}
