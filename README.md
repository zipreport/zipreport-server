# zipreport-server

ZipReport-server is the [zipreport](https://github.com/zipreport/zipreport) HTML to PDF conversion daemon, based on
[rod](https://github.com/go-rod/rod) and Chromium, written in Go.

**Note:** zipreport-server 2.xx only works with zipreport library version >= 2.0.0 ,and it is **incompatible** with
previous
versions.

### Security considerations

zipreport-server relies on Chromium to render artifacts into PDF. As such, it allows unfetered execution of any
external dependencies and scripts your template may use. This behaviour may pose a security risk on certain
environments.
The daemon also relies on the creation of ephemeral http servers on localhost as part of the rendering process.

### How it works

The zipreport-server API receives a rendering request with an associated ZPT resource. For each rendering request,
zipreport-server
launches an internal http server to serve the ZPT content, and then instructs a Chromium instance to open the temporary
url and render to PDF.

The settling time for the internal HTML/JS rendering process can either be a default value in milisseconds (the default
behaviour), or triggered by writing 'zpt-view-ready' to the JS console. By using the console approach, the PDF generation
is triggered only after all dynamic canvas elements were generated.

### Command line options

| Option              | Mandatory | Description                                                    |
|---------------------|-----------|----------------------------------------------------------------|
| -addr=\<address\>   | No        | Address to listen (default *)                                  |
| -port=\<port\>      | No        | Port to listen (default 6543)                                  | 
| -keyfile=\<path\>   | No        | SSL certificate key file                                       |
| -crtfile=\<path\>   | No        | SSL certificate file                                           |
| -apikey=\<key\>     | No        | API key for authentication (via X-Auth-Key)                    |
| -httprt=\<seconds\> | No        | Http server read timeout, in seconds (default 300)             |
| -httpwt=\<seconds\> | No        | Http server write timeout, in seconds (default 300)            |
| -debug              | No        | Enable verbose output                                          |
| -nometrics          | No        | Disable Prometheus metric endpoint                             |
| -version            | No        | Show current zipreport-server version                          |
| -concurrency        | No        | Maximum browser instance count (default 8)                     |
| -baseport           | No        | Base port to be used for internal HTTP servers (default 42000) |
| -loglevel           | No        | Log level (default 2/WARN)                                     |

### Available endpoints

#### [POST] /v2/render

**Format:** multipart/form-data

**Fields:**

| Field             | Mandatory | Description                                                   |
|-------------------|-----------|---------------------------------------------------------------|
| report            | Yes       | Report file                                                   |
| page_size         | Yes       | Page size (A5/A4/A3/Letter/Legal/Tabloid)                     |
| margins           | Yes       | Margin type (none/minimal/standard)                           |
| landscape         | No        | If true, print in landscape                                   |
| script            | No        | Main html file (default report.html)                          |
| settling_time     | No        | Settling time, in ms (default 200ms, see below)               |
| timeout_job       | No        | Job timeout, in seconds (default 120s, see below)             | 
| timeout_js        | No        | JavaScript event timeout, in seconds (default 30s, see below) |
| js_event          | No        | If true, wait for the javascript event (see below)            |
| ignore_ssl_errors | No        | If true, ssl errors in referenced resources will be ignored   |

**settling_time** (default: 200)

Value in ms to wait after the DOM is ready to print the report. This setting is ignored if
js_event is enabled.

**timeout_job** (default 120)

Waiting time, in seconds, to perform the conversion operation, including waiting times such as
settling_time.

**timeout_js** (default 30)

Time to wait, in seconds, for the javascript event to be triggered, before generating the
report anyway. Requires js_event to be true.

**js_event**

If true, the system will wait upto timeout_js for a specific console message before
generating the PDF. This allows for dynamic pages to signal when DOM manipulation is finished.

Triggering example:

```javascript
<script>
    (function() {
    // signal end of composition
    console.log("zpt-view-ready")
})();
</script>
```

#### [GET] /metrics

Prometheus metrics endpoint. Besides the default internal Go metrics, the following are provided:

| metric                | type      | description                                                           |
|-----------------------|-----------|-----------------------------------------------------------------------|
| total_request_success | counter   | Number of successful API calls                                        |
| total_request_error   | counter   | Number of failed API calls                                            |
| conversion_time       | histogram | Elapsed conversion time histogram, in seconds. The upper bound is 120 |
| current_http_servers  | gauge     | Current internal HTTP server count                                    |
| current_browsers      | gauge     | Current internal browser instance count                               |

### Authentication

If -apikey is specified, zipreport-server will perform header-based authentication with
the designated key. Clients should pass the key in the "X-Auth-Key" header.

### Running with Docker


**Available Environment Variables**

| name                      | Description                                                                |
|---------------------------|----------------------------------------------------------------------------|
| ZIPREPORT_API_PORT        | Port for the API to listen (default 6543)                                  |
| ZIPREPORT_API_KEY         | API authentication key                                                     |
| ZIPREPORT_BASE_PORT       | Internal base HTTP port to use for browser content serving (default 42000) |
| ZIPREPORT_SSL_CERTIFICATE | Optional SSL certificate, to be used instead of the self-signed one        |
| ZIPREPORT_SSL_KEY         | Optional SSL certificate key, to be used instead of the self-signed one    |
|ZIPREPORT_CONCURRENCY| Number of simultaneus browser instances to use (default 8)                 |
|ZIPREPORT_DEBUG| Enable API debug mode|
|ZIPREPORT_LOGLEVEL| Set zipreport-server log level|

**Build locally**
```shell
$ docker build . --tag zipreport-server:latest
$ docker run -p 6543:6543 zipreport-server \
    -e ZIPREPORT_API_KEY="my-api-mey" \
    -e ZIPREPORT_DEBUG="true"
```

**Use prebuilt image**
```shell
$ docker pull ghcr.io/zipreport/zipreport-server:latest
$ docker run -p 6543:6543 ghcr.io/zipreport/zipreport-server:latest \
    -e ZIPREPORT_API_KEY="my-api-mey" \
    -e ZIPREPORT_DEBUG="true"
```


### Build

To build the binary in ./bin as well as a self-signed certificate in ./cert, just run
make:

```shell script
$ make all
$ make certificate
```
