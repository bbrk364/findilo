package main

import (
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cheggaaa/pb"
	"github.com/olekukonko/tablewriter"
	"github.com/parnurzeal/gorequest"
)

const (
	iloPort      = 17988
	notAvailable = "N/A"
)

var (
	ipNetParsed []string
)

// ILOInfo ...
type ILOInfo struct {
	IP         string
	HW         string
	Model      string
	FW         string
	Serial     string
	ServerName string
	IloName    string
}

// ILOSorter ...
type ILOSorter struct {
	ilo []ILOInfo
	by  func(i1, i2 *ILOInfo) bool
}

// By is the type of a "less" function that defines the ordering of its Planet arguments.
type By func(i1, i2 *ILOInfo) bool

// Sort is a method on the function type, By, that sorts the argument slice according to the function.
func (by By) Sort(ilo []ILOInfo) {
	ps := &ILOSorter{
		ilo: ilo,
		by:  by, // The Sort method's receiver is the function (closure) that defines the sort order.
	}
	sort.Sort(ps)
}

// Len is part of sort.Interface.
func (s *ILOSorter) Len() int {
	return len(s.ilo)
}

// Swap is part of sort.Interface.
func (s *ILOSorter) Swap(i, j int) {
	s.ilo[i], s.ilo[j] = s.ilo[j], s.ilo[i]
}

// Less is part of sort.Interface. It is implemented by calling the "by" closure in the sorter.
func (s *ILOSorter) Less(i, j int) bool {
	return s.by(&s.ilo[i], &s.ilo[j])
}

// RIMP ...
type RIMP struct {
	XMLName xml.Name `xml:"RIMP"`
	SBSN    string   `xml:"HSI>SBSN"`
	SPN     string   `xml:"HSI>SPN"`
	PN      string   `xml:"MP>PN"`
	FWRI    string   `xml:"MP>FWRI"`
	HWRI    string   `xml:"MP>HWRI"`
}

// ServerName ...
type ServerName struct {
	Name string `json:"server_name"`
	Cn   string `json:"cn"`
}

// HW ...
func (r *RIMP) HW() string {
	var iloRevision = regexp.MustCompile(`\((.*)\)`)
	if len(r.PN) == 0 {
		return notAvailable
	}
	return strings.TrimSpace(iloRevision.FindAllStringSubmatch(r.PN, 1)[0][1])
}

// Model ...
func (r *RIMP) Model() string {
	if len(r.SPN) == 0 {
		return notAvailable
	}
	return strings.TrimSpace(r.SPN)
}

// FW ...
func (r *RIMP) FW() string {
	if len(r.FWRI) == 0 {
		return notAvailable
	}
	return strings.TrimSpace(r.FWRI)
}

func inc(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

// IsOpen ...
func IsOpen(host string, port int) bool {

	tcpAddr, err := net.ResolveTCPAddr("tcp4", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", tcpAddr.String(), 250*time.Millisecond)

	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

func requestServerNameV2(ip string) (string, string, error) {
	request := gorequest.New()

	_, body, err := request.Get(fmt.Sprintf("http://%s/", ip)).End()
	if err != nil {
		return "", "", fmt.Errorf("%v", err)
	}
	reSrv := regexp.MustCompile(`serverName\="([\w-]+)"`)
	reIlo := regexp.MustCompile(`nicName\="([\w-]+)"`)
	matchSrv := reSrv.FindStringSubmatch(string(body))
	matchIlo := reIlo.FindStringSubmatch(string(body))
	serverName := ""
	iloName := ""
	if len(matchSrv) > 0 {
		serverName = matchSrv[1]
	}
	if len(matchIlo) > 0 {
		iloName = matchIlo[1]
	}
	return serverName, iloName, nil
}

func requestServerName(ip string) (string, string, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://%s/json/login_session?null", ip), nil)
	req.Header.Set("Content-Type", "application/json")
	if err != nil {
		return "", "", err
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}

	raw, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	srvinfo := &ServerName{}
	if err := json.Unmarshal(raw, srvinfo); err != nil {
		return "", "", err
	}
	//fmt.Println(srvinfo.ServerName)
	return srvinfo.Name, srvinfo.Cn, nil
}

func requestInfo(ip string) (*ILOInfo, error) {
	request := gorequest.New()
	rinfo := &RIMP{}

	_, body, err := request.Get(fmt.Sprintf("http://%s/xmldata?item=all", ip)).End()

	if err != nil {
		return nil, fmt.Errorf("%v", err)
	}
	if err := xml.Unmarshal([]byte(body), rinfo); err != nil {
		return nil, err
	}
	return &ILOInfo{
		IP:     ip,
		HW:     rinfo.HW(),
		FW:     rinfo.FW(),
		Model:  rinfo.Model(),
		Serial: strings.TrimSpace(rinfo.SBSN),
	}, nil
}

func makeJobs(ar []string, count int) [][]string {
	chunk := len(ar) / count
	start := 0
	end := count
	res := [][]string{}
	for end < len(ar) {
		res = append(res, ar[start:end])
		start = end
		end += chunk
	}
	res = append(res, ar[start:len(ar)])
	return res
}
func tableRender(ilo []ILOInfo) {
	data := [][]string{}
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"IP", "HW", "FW", "S/N", "Model", "ServerName", "Name"})
	table.SetBorder(false) // Set Border to false
	version := func(i1, i2 *ILOInfo) bool {
		i1s := strings.Split(i1.HW, " ")
		i2s := strings.Split(i2.HW, " ")
		i1v := 1
		i2v := 1
		if len(i1s) > 1 {
			i1v, _ = strconv.Atoi(i1s[1])
		}
		if len(i2s) > 1 {
			i2v, _ = strconv.Atoi(i2s[1])
		}

		return i1v < i2v
	}
	By(version).Sort(ilo)
	for _, info := range ilo {
		data = append(data, []string{
			info.IP,
			info.HW,
			info.FW,
			info.Serial,
			info.Model,
			info.ServerName,
			info.IloName,
		})
	}

	table.AppendBulk(data) // Add Bulk Data
	fmt.Println("")
	table.Render()
}

func scan(ips []string, out chan ILOInfo, bar *pb.ProgressBar, wg *sync.WaitGroup) {
	for _, host := range ips {
		if IsOpen(host, iloPort) {
			srvName := ""
			iloName := ""
			info, err := requestInfo(host)
			if err != nil {
				fmt.Println(err)
			}
			if match, _ := regexp.MatchString("iLO (3|4|5)", info.HW); match {
				srvName, iloName, _ = requestServerName(host)
			} else {
				srvName, iloName, _ = requestServerNameV2(host)
			}
			info.ServerName = srvName
			info.IloName = iloName
			out <- *info
		}
		bar.Increment()
	}
	wg.Done()
}

func main() {
	if len(os.Args) == 1 {
		fmt.Printf("Usage: findilo <networks>, Format 10.0.0.0/24\n")
		os.Exit(1)
	}
	var ips []string
	for _, ipNetwork := range os.Args[1:] {
		ip, ipnet, err := net.ParseCIDR(ipNetwork)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); inc(ip) {
			ips = append(ips, ip.String())
		}
	}
	ipNetParsed = ips
	jobs := makeJobs(ipNetParsed, 100)
	out := make(chan ILOInfo, 100)
	ipNetLen := len(ipNetParsed)

	scanbar := pb.StartNew(ipNetLen)
	scanbar = scanbar.Prefix("Scan net")
	scanbar.ShowTimeLeft = false

	wg := new(sync.WaitGroup)
	//Запуск воркеров
	for _, job := range jobs {
		wg.Add(1)
		go scan(job, out, scanbar, wg)
	}

	wg.Wait()
	close(out)

	ilo := []ILOInfo{}
	for info := range out {
		ilo = append(ilo, info)
	}
	scanbar.Finish()
	tableRender(ilo)
	fmt.Println("")
}
