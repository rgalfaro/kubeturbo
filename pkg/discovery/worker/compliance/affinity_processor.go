package compliance

import (
	api "k8s.io/client-go/pkg/api/v1"

	"github.com/turbonomic/kubeturbo/pkg/cluster"
	"github.com/turbonomic/kubeturbo/pkg/discovery/util"

	sdkbuilder "github.com/turbonomic/turbo-go-sdk/pkg/builder"
	"github.com/turbonomic/turbo-go-sdk/pkg/proto"

	"github.com/golang/glog"
)

type affinityProcessorConfig struct {
	k8sClusterScraper *cluster.ClusterScraper
}

func NewAffinityProcessorConfig(k8sClusterScraper *cluster.ClusterScraper) *affinityProcessorConfig {
	return &affinityProcessorConfig{
		k8sClusterScraper: k8sClusterScraper,
	}
}

type AffinityProcessor struct {
	*ComplianceProcessor

	commManager *AffinityCommodityManager

	nodes []*api.Node
	pods  []*api.Pod
}

func NewAffinityProcessor(config *affinityProcessorConfig) (*AffinityProcessor, error) {
	allNodes, err := config.k8sClusterScraper.GetAllNodes()
	if err != nil {
		return nil, err
	}
	allPods, err := config.k8sClusterScraper.GetAllPods()
	if err != nil {
		return nil, err
	}
	return &AffinityProcessor{
		ComplianceProcessor: NewComplianceProcessor(),
		commManager:         NewAffinityCommodityManager(),

		nodes: allNodes,
		pods:  allPods,
	}, nil
}

// TODO if there is error, fail the whole discovery? currently, error is handle in place and won't affect other discovery results.
func (am *AffinityProcessor) ProcessAffinityRules(entityDTOs []*proto.EntityDTO) []*proto.EntityDTO {
	am.GroupEntityDTOs(entityDTOs)
	for _, pod := range am.pods {
		am.processAffinityPerPod(pod)
	}
	return am.GetAllEntityDTOs()
}

func (am *AffinityProcessor) processAffinityPerPod(pod *api.Pod) {
	affinity := pod.Spec.Affinity
	if affinity == nil {
		return
	}

	nodeMap := make(map[string]*api.Node)
	for _, currNode := range am.nodes {
		nodeMap[currNode.Name] = currNode
	}

	podsNodesMap := make(map[*api.Pod]*api.Node)
	for _, currPod := range am.pods {
		hostingNode, exist := nodeMap[currPod.Spec.NodeName]
		if !exist || hostingNode == nil {
			continue
		}
		podsNodesMap[currPod] = hostingNode
	}

	nodeSelectorTerms := getAllNodeSelectors(affinity)
	nodeAffinityAccessCommoditiesSold, nodeAffinityAccessCommoditiesBought, err := am.commManager.GetAccessCommoditiesForNodeAffinity(nodeSelectorTerms)
	if err != nil {
		glog.Errorf("Failed to build commodity: %s", err)
		return
	}

	podAffinityTerms := getAllPodAffinityTerms(affinity)
	podAffinityCommodityDTOsSold, podAffinityCommodityDTOsBought, err := am.commManager.GetAccessCommoditiesForPodAffinityAntiAffinity(podAffinityTerms)
	if err != nil {
		glog.Errorf("Failed to build commodity for pod affinity: %s", err)
		return
	}

	for _, node := range am.nodes {
		if matchesNodeSelector(pod, node) && matchesNodeAffinity(pod, node) {
			am.addAffinityAccessCommodities(pod, node, nodeAffinityAccessCommoditiesSold, nodeAffinityAccessCommoditiesBought)
		}
		if interPodAffinityMatches(pod, node, podsNodesMap) {
			am.addAffinityAccessCommodities(pod, node, podAffinityCommodityDTOsSold, podAffinityCommodityDTOsBought)
		}
	}
}

func getAllNodeSelectors(affinity *api.Affinity) []api.NodeSelectorTerm {
	nodeSelectorTerms := []api.NodeSelectorTerm{}
	// TODO we only parse RequiredDuringSchedulingIgnoredDuringExecution for now.
	if affinity.NodeAffinity != nil && affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		nodeSelectorTerms = append(nodeSelectorTerms, affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms...)
	}
	//glog.Infof("node selectors are %++v", nodeSelectorTerms)
	return nodeSelectorTerms
}

func getAllPodAffinityTerms(affinity *api.Affinity) []api.PodAffinityTerm {
	podAffinityTerms := []api.PodAffinityTerm{}
	// TODO we only parse RequiredDuringSchedulingIgnoredDuringExecution for now.
	if affinity.PodAffinity != nil && affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		podAffinityTerms = append(podAffinityTerms, affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution...)
	}
	// TODO we only parse RequiredDuringSchedulingIgnoredDuringExecution for now.
	if affinity.PodAntiAffinity != nil && affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		podAffinityTerms = append(podAffinityTerms, affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution...)
	}
	//glog.Infof("pod selector terms are %++v", podAffinityTerms)
	return podAffinityTerms
}

func (am *AffinityProcessor) addAffinityAccessCommodities(pod *api.Pod, node *api.Node,
	affinityAccessCommoditiesSold, affinityAccessCommoditiesBought []*proto.CommodityDTO) {

	// add commodity sold by matching nodes.
	if affinityAccessCommoditiesSold != nil && len(affinityAccessCommoditiesSold) > 0 {
		am.addCommoditySoldByNode(node, affinityAccessCommoditiesSold)
	}

	// add commodity bought by pod.
	if pod.Spec.NodeName == node.Name &&
		affinityAccessCommoditiesBought != nil && len(affinityAccessCommoditiesBought) > 0 {
		am.addCommodityBoughtByPod(pod, node, affinityAccessCommoditiesBought)
	} // end if
}

func (am *AffinityProcessor) addCommoditySoldByNode(node *api.Node, affinityAccessCommodityDTOs []*proto.CommodityDTO) {
	nodeEntityDTO, err := am.GetEntityDTO(proto.EntityDTO_VIRTUAL_MACHINE, string(node.UID))
	if err != nil {
		glog.Errorf("Cannot find the entityDTO: %s", err)
		return
	}
	err = am.AddCommoditiesSold(nodeEntityDTO, affinityAccessCommodityDTOs...)
	if err != nil {
		glog.Errorf("Failed to add commodityDTO to %s: %s", node.Name, err)
	}
}

func (am *AffinityProcessor) addCommodityBoughtByPod(pod *api.Pod, node *api.Node, affinityAccessCommodityDTOs []*proto.CommodityDTO) {
	podEntityDTO, err := am.GetEntityDTO(proto.EntityDTO_CONTAINER_POD, string(pod.UID))
	if err != nil {
		glog.Errorf("Cannot find the entityDTO: %s", err)
		return
	}
	provider := sdkbuilder.CreateProvider(proto.EntityDTO_VIRTUAL_MACHINE, string(node.UID))
	err = am.AddCommoditiesBought(podEntityDTO, provider, affinityAccessCommodityDTOs...)
	if err != nil {
		glog.Errorf("Failed to add commodityDTOs to %s: %s", util.GetPodClusterID(pod), err)
	}
}
