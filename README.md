[![Build Status](https://fleet-ci.elastic.co/job/fleet-server/job/fleet-server-mbp/job/main/badge/icon)](https://fleet-ci.elastic.co/job/Ingest-manager/job/fleet-server/job/main/)

# Fleet Server implementation

## Development

fleet-server is under development. The following are notes to help developers onboarding to the project to quickly get running. These notes might change at any time.

## Setup for fleet-server development

### ES and Kinaba from SNAPSHOTS API

Download them from the snapshots API:
Edit the version and OS/arch to suit your system, or [check the API](https://artifacts-api.elastic.co/v1/search/8.4-SNAPSHOT) (change the version if needed) if the ones below does not suit you.
 - 8.4.0-SNAPSHOT-linux-x86_64.tar.gz
 - 8.4.0-SNAPSHOT-darwin-aarch64.tar.gz
 - 8.4.0-SNAPSHOT-windows-x86_64.zip

TODO: parse the JSON to get the URL
```shell
wget https://snapshots.elastic.co/8.4.0-64eb2b35/downloads/elasticsearch/elasticsearch-8.4.0-SNAPSHOT-linux-x86_64.tar.gz
wget https://snapshots.elastic.co/8.4.0-64eb2b35/downloads/kibana/kibana-8.4.0-SNAPSHOT-linux-x86_64.tar.gz
```

Follow the instructions for [ElasticSearch](https://www.elastic.co/downloads/elasticsearch) and [Kibana](https://www.elastic.co/downloads/kibana)

### fleet-server+agent on a Vagrant VM

The Vagrant machine assumes the `elastic-agent`, `beats` and `fleet-server` repos are in the same folder.
Thus, it mounts `../` to `/vagrant` on the Vagrant machine. The vagrant machine IP address is `192.168.56.43`.
Use `https://192.168.56.43:8220` as fleet-server host.

### Grab or build the Elastic-Agent

For detailed instructions, check the [Elastic-Agent](https://github.com/elastic/elastic-agent) repo.
```shell
cd YOUR_ELASTIC_AGENT_FOLDER
SNAPSHOT=true EXTERNAL=true PLATFORMS="linux/amd64" PACKAGES="tar.gz" mage -v dev:package # adjust PLATFORMS and PACKAGES to your system and needs.
```

### Build and Package

Change `release-linux/amd64` to `release-YOUR_OS/platform`. Run `make list-platforms` to check
out the possible values.

The `DEV=true` will allow the binary to be debugged ~~with a debugger~~.

```shell
DEV=true SNAPSHOT=true make release-linux/amd64

vagrant up
vagrant ssh
```

For the Elastic-Agent to use your build of the fleet-server, unpack the elastic agent and add your fleet-server tar.gz and sha512 to
`elastic-agent-8.Y.Z-SNAPSHOT-OS-ARCH/data/elastic-agent-*/downloads/`. 

Then go to `Kibana > Managment > Fleet` and follow the instructions there.

#### tl;dr/example:
```shell
cp /vagrant/elastic-agent/build/distributions/elastic-agent-8.4.0-SNAPSHOT-linux-x86_64.tar.gz* ./
tar xzvf elastic-agent-8.4.0-SNAPSHOT-linux-x86_64.tar.gz
cd elastic-agent-8.4.0-SNAPSHOT-linux-x86_64
cp /vagrant/fleet-server/build/distributions/fleet-server-8.4.0-SNAPSHOT-linux-x86_64.tar.gz* ./data/elastic-agent-494b79/downloads/
./elastic-agent install ...
```

## Setup for Elastic-Agent development

### From source
To run and test fleet-server, a recent version of Elastic Agent and Kibana are needed. In the following Elastic Agent and Kibana are built from source. The fleet-server itself is not built from source but pulled from the latest snapshot build. It would be possible to also pull Elastic Agent or Kibana from the latest snapshot but the assumption that is made here that whoever is testing this, is likely developing either Elastic Agent or on the Kibana side.


### Kibana setup

The source code of Kibana must be checked out. After checkout, the following command must be run:

```
yarn kbn bootstrap
```

This will take a while the first time it is run. An error might be return in case not a valid node version is installed. Use nvm to install the correct version.

Now the following two commands must be run in parallel:

```
# Start ES
yarn es snapshot -E xpack.security.authc.api_key.enabled=true

# Start KB
yarn start --no-base-path
```

As soon as all is running, go to `http://localhost:5601`, enter `elastic/changeme` as credential and navigate to Fleet. Trigger the Fleet setup. As soon as this is completed, copy the `policy id` and `enrollment token` for the fleet-server policy. The policy id can be copied from the URL, the enrollment token can be found in the Enrollment Token list.

NOTE: This step can be skipped if the full command below for the Elastic Agent is used.

Now Kibana is running and ready. The next step is to setup Elastic Agent.

## Elastic-Agent

To build the Elastic Agent from source, check out the [elastic-agent repository](https://github.com/elastic/elastic-agent), then run the following command:

```
SNAPSHOT=true DEV=true PLATFORMS=linux|darwin|windows mage package
```

The above assumes you are running on OS X. Set `PLATFORMS` to the one in you are running on. This speeds up packaging as it only builds it for your platform. As soon as this is completed (it might take a while for the first time) navigate to `build/distributions` and unpackage the `.tar.gz`. Change working directory to the elastic-agent directory and start the Elastic Agent:

```
KIBANA_HOST=http://localhost:5601 KIBANA_USERNAME=elastic KIBANA_PASSWORD=changeme ELASTICSEARCH_HOST=http://localhost:9200 ELASTICSEARCH_USERNAME=elastic ELASTICSEARCH_PASSWORD=changeme KIBANA_FLEET_SETUP=1 FLEET_SERVER_ENABLE=1 sudo ./elastic-agent container
```

This will start up Elastic Agent with fleet-server and directly enroll it. In addition, Fleet is set up inside Kibana. In case the setup is done already in Kibana manually, the following command can be used:

```
sudo ./elastic-agent enroll --fleet-server=http://elastic:changeme@localhost:9200 --fleet-server-policy={fleet-server-policy-id} --enrollment-token={policy-enrollment-token}
```

## Running Elastic Agent with fleet-server in container

If you want to run Elastic Agent and fleet-server in a container but built Kibana from source, you have to add the following to your `config/kibana.dev.yml`:

```
server.host: 0.0.0.0
```

This makes sure, Kibana is accessible from the container. Start Kibana as before but for Elasticsearch, run the following command:

```
yarn es snapshot -E xpack.security.authc.api_key.enabled=true -E http.host=0.0.0.0
```

This makes sure also Elasticsearch is accessible to the container.

Start the Elastic Agent with the following command:

```
docker run -e KIBANA_HOST=http://{YOUR-IP}:5601 -e KIBANA_USERNAME=elastic -e KIBANA_PASSWORD=changeme -e ELASTICSEARCH_HOST=http://{YOUR-IP}:9200 -e ELASTICSEARCH_USERNAME=elastic -e ELASTICSEARCH_PASSWORD=changeme -e KIBANA_FLEET_SETUP=1 -e FLEET_SERVER_ENABLE=1 -e FLEET_SERVER_INSECURE_HTTP=1 docker.elastic.co/beats/elastic-agent:8.0.0-SNAPSHOT
```

Replace {YOUR-IP} with the IP address of your machine.

## fleet-server repo

By default the above will download the most recent snapshot build for fleet-server. To use your own development build, run `make release` in the fleet-server repository, go to `build/distributions` and copy the `.tar.gz` and `sha512` file to the `data/elastic-agent-{hash}/downloads` inside the elastic-agent directory. Now you run with your own build of fleet-server.


## Compatibility and upgrades

Fleet server is always on the exact same version as Elastic Agent running fleet-server. Any Elastic Agent enrolling into a fleet-server must be the same version or older. Fleet-server communicates with Elasticsearch. Elasticsearch must be on the same version or newer. For Kibana it is assumed it is on the same version as Elasticsearch. With this the compatibility looks as following:

```
Elastic Agent <= Elastic Agent with fleet-server) <= Elasticsearch / Kibana
```

There might be differences on the bugfix version.

If an upgrade is done, Elasticsearch / Kibana have to be upgraded first, then Elastic Agent with fleet-server and last the Elastic Agents.


## MacOSX Version

The [golang-crossbuild](https://github.com/elastic/golang-crossbuild) produces images used for testing/building.
The `golang-crossbuild:1.16.X-darwin-debian10` images expects the minimum MacOSX version to be 10.14+.
