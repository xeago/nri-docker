package docker

import (
	"context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/newrelic/infra-integrations-sdk/data/metric"
	"github.com/newrelic/infra-integrations-sdk/integration"
	"github.com/newrelic/infra-integrations-sdk/log"
	"github.com/newrelic/nri-docker/src/biz"
	"github.com/newrelic/nri-docker/src/stats"
)

const labelPrefix = "label."
const dockerClientVersion = "1.24" // todo: make configurable

type ContainerSampler struct {
	docker  *client.Client
	stats   stats.Provider
	network stats.NetworkFetcher
}

func populate(ms *metric.Set, metrics []Metric) error {
	for _, metric := range metrics {
		if err := ms.SetMetric(metric.Name, metric.Value, metric.Type); err != nil {
			return err
		}
	}
	return nil
}

func attributes(container types.Container) []Metric {
	var cname string
	if len(container.Names) > 0 {
		cname = container.Names[0]
		if len(cname) > 0 && cname[0] == '/' {
			cname = cname[1:]
		}
	}
	return []Metric{
		MetricCommandLine(container.Command),
		MetricContainerName(cname),
		MetricContainerImage(container.ImageID),
		MetricContainerImageName(container.Image),
		MetricState(container.State),
		MetricStatus(container.Status),
	}
}

func labels(container types.Container) []Metric {
	metrics := make([]Metric, 0, len(container.Labels))
	for key, val := range container.Labels {
		metrics = append(metrics, Metric{
			Name:  labelPrefix + key,
			Value: val,
			Type:  metric.ATTRIBUTE,
		})
	}
	return metrics
}

func memory(mem biz.Memory) []Metric {
	return []Metric{
		MetricMemoryCacheBytes(mem.CacheUsageBytes),
		MetricMemoryUsageBytes(mem.UsageBytes),
		MetricMemoryResidentSizeBytes(mem.RSSUsageBytes),
		MetricMemorySizeLimitBytes(mem.MemLimitBytes),
	}
}

func pids(pids biz.Pids) []Metric {
	return []Metric{
		MetricProcessCount(pids.Current),
		MetricProcessCountLimit(pids.Limit),
	}
}

func blkio(bio biz.BlkIO) []Metric {
	return []Metric{
		MetricIOTotalReadCount(bio.TotalReadCount),
		MetricIOTotalWriteCount(bio.TotalWriteCount),
		MetricIOTotalReadBytes(bio.TotalReadBytes),
		MetricIOTotalWriteBytes(bio.TotalWriteBytes),
		MetricIOTotalBytes(bio.TotalReadBytes + bio.TotalWriteBytes),
		MetricIOReadCountPerSecond(bio.TotalReadCount),
		MetricIOWriteCountPerSecond(bio.TotalWriteCount),
		MetricIOReadBytesPerSecond(bio.TotalReadBytes),
		MetricIOWriteBytesPerSecond(bio.TotalWriteBytes),
	}
}

func (cs *ContainerSampler) networkMetrics(net biz.Network) []Metric {
	return []Metric{
		MetricRxBytes(net.RxBytes),
		MetricRxErrors(net.RxErrors),
		MetricRxDropped(net.RxDropped),
		MetricRxPackets(net.RxPackets),
		MetricTxBytes(net.TxBytes),
		MetricTxErrors(net.TxErrors),
		MetricTxDropped(net.TxDropped),
		MetricTxPackets(net.TxPackets),
		MetricRxBytesPerSecond(net.RxBytes),
		MetricRxErrorsPerSecond(net.RxErrors),
		MetricRxDroppedPerSecond(net.RxDropped),
		MetricRxPacketsPerSecond(net.RxPackets),
		MetricTxBytesPerSecond(net.TxBytes),
		MetricTxErrorsPerSecond(net.TxErrors),
		MetricTxDroppedPerSecond(net.TxDropped),
		MetricTxPacketsPerSecond(net.TxPackets),
	}
}

func cpu(cpu biz.CPU) []Metric {
	return []Metric{
		MetricCPUUsedCores(cpu.UsedCores),
		MetricCPUUsedCoresPercent(cpu.UsedCoresPercent),
		MetricCPULimitCores(cpu.LimitCores),
		MetricCPUPercent(cpu.CPUPercent),
		MetricCPUKernelPercent(cpu.KernelPercent),
		MetricCPUUserPercent(cpu.UserPercent),
		MetricCPUThrottlePeriods(cpu.ThrottlePeriods),
		MetricCPUThrottleTimeMS(cpu.ThrottledTimeMS),
	}
}

func inspectData(json types.ContainerJSON) []Metric {
	return []Metric{
		MetricRestartCount(json.RestartCount),
	}
}

func NewContainerSampler(statsProvider stats.Provider) (ContainerSampler, error) {
	cli, err := client.NewEnvClient()
	if err != nil {
		return ContainerSampler{}, err
	}
	cli.UpdateClientVersion(dockerClientVersion) // TODO: make it configurable
	net, err := stats.NewNetworkFetcher()
	if err != nil {
		return ContainerSampler{}, err
	}
	return ContainerSampler{
		docker:  cli,
		stats:   statsProvider,
		network: net,
	}, nil
}

func (cs *ContainerSampler) SampleAll(i *integration.Integration) error {
	containers, err := cs.docker.ContainerList(context.Background(), types.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, container := range containers {

		entity, err := i.Entity(container.ID, "docker")
		if err != nil {
			return err
		}

		ms := entity.NewMetricSet(ContainerSampleName,
			metric.Attr("hostname", "localhost"), // will be replaced by the agent
			metric.Attr(AttrContainerID, container.ID))

		_ = populate(ms, fakeMetrics()) // TODO: remove

		if err := populate(ms, attributes(container)); err != nil {
			log.Debug("error populating container %v attributes: %s", container.ID, err)
			continue
		}

		if err := populate(ms, labels(container)); err != nil {
			log.Debug("error populating container %v labels: %s", container.ID, err)
			continue
		}

		cjson, err := cs.docker.ContainerInspect(context.Background(), container.ID)
		if err != nil {
			log.Debug("error inspecting container %v: %s", container.ID, err)
			continue
		}

		if err := populate(ms, inspectData(cjson)); err != nil {
			log.Debug("error populating container %v inspect data: %s", container.ID, err)
			continue
		}

		stats, err := cs.stats.Fetch(container.ID)
		if err != nil {
			log.Error("error retrieving stats for container %s: %s", container.ID, err.Error())
			continue
		}
		if err := populate(ms, statsMetrics(stats)); err != nil {
			log.Debug("error populating container %v stats metrics: %s", container.ID, err)
			continue
		}
		if err := populate(ms, cpu(stats, cjson)); err != nil {
			log.Debug("error populating container %v CPU core metrics: %s", container.ID, err)
			continue
		}
		if err := populate(ms, cs.networkMetrics(cjson.State.Pid)); err != nil {
			log.Debug("error populating container %v network metrics: %s", container.ID, err)
			continue
		}

	}
	return nil
}

// TODO: remove
func fakeMetrics() []Metric {
	var fake = func(name string, value interface{}) Metric {
		return Metric{Name: name, Type: metric.ATTRIBUTE, Value: value}
	}
	return []Metric{
		fake("linuxDistribution", "CentOS Linux 7 (Core)"),
		fake("agentVersion", "1.5.37"),
		fake("systemMemoryBytes", "1927303168"),
		fake("coreCount", "2"),
		fake("fullHostname", "ohai1.new-domain.com"),
		fake("kernelVersion", "3.10.0-957.27.2.el7.x86_64"),
		fake("processorCount", "2"),
		{Name: "warningViolationCount", Type: metric.GAUGE, Value: 0},
		fake("agentName", "Infrastructure"),
		fake("operatingSystem", "linux"),
		{Name: "criticalViolationCount", Type: metric.GAUGE, Value: 0},
		fake("instanceType", "unknown"),
	}
}
