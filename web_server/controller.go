package web_server

import (
	"errors"
	"fmt"
	"net/http"

	client "github.com/cloudfoundry-samples/go_service_broker/client"
	utils "github.com/cloudfoundry-samples/go_service_broker/utils"
	model "github.com/cloudfoundry-samples/go_service_broker/model"
)

const (
	DEFAULT_POLLING_INTERVAL_SECONDS = 10
)

type Controller struct {
	cloudName string
	cloudClient client.Client

	instanceMap map[string]*model.ServiceInstance
	bindingMap  map[string]*model.ServiceBinding
}

func CreateController(cloudName string, instanceMap map[string]*model.ServiceInstance, bindingMap map[string]*model.ServiceBinding) (*Controller, error) {
	cloudClient, err := createCloudClient(cloudName)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("Could not create cloud: %s client, message: %s", cloudName, err.Error()))
	}

	return &Controller{		
		cloudName: cloudName,
		cloudClient: cloudClient,

		instanceMap:   instanceMap,
		bindingMap:    bindingMap,
	}, nil
}

func (c *Controller) Catalog(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Get Service Broker Catalog...")

	var catalog model.Catalog
	catalogFileName := "catalog.json"

	if c.cloudName == utils.AWS {
		catalogFileName = "catalog.AWS.json"
	} else if c.cloudName == utils.SOFTLAYER || c.cloudName == utils.SL {
		catalogFileName = "catalog.SoftLayer.json"
	}

	err := utils.ReadAndUnmarshal(&catalog, conf.CatalogPath, catalogFileName)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	utils.WriteResponse(w, http.StatusOK, catalog)
}

func (c *Controller) CreateServiceInstance(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Create Service Instance...")

	var instance model.ServiceInstance

	err := utils.ProvisionDataFromRequest(r, &instance)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	fmt.Println("Provision data")
	instanceId, err := c.cloudClient.CreateInstance(instance.Parameters)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	instance.InternalId = instanceId
	instance.DashboardUrl = "http://dashbaord_url"
	instance.Id = utils.ExtractVarsFromRequest(r, "service_instance_guid")
	instance.LastOperation = &model.LastOperation{
		State:                    "in progress",
		Description:              "creating service instance...",
		AsyncPollIntervalSeconds: DEFAULT_POLLING_INTERVAL_SECONDS,
	}

	c.instanceMap[instance.Id] = &instance
	err = utils.MarshalAndRecord(c.instanceMap, conf.DataPath, conf.ServiceInstancesFileName)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	response := model.CreateServiceInstanceResponse{
		DashboardUrl:  instance.DashboardUrl,
		LastOperation: instance.LastOperation,
	}
	utils.WriteResponse(w, http.StatusAccepted, response)
}

func (c *Controller) GetServiceInstance(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Get Service Instance State....")

	instanceId := utils.ExtractVarsFromRequest(r, "service_instance_guid")
	instance := c.instanceMap[instanceId]
	var state string
	var err error
	var failedOperation model.LastOperation
	if instance == nil {
		fmt.Println("No instance found with id: "+instanceId)
		state = "unknown"
		failedOperation.State = "failed"
		failedOperation.Description = "The service instance has been deleted from the backend"
		
		response := failedOperation
		utils.WriteResponse(w, http.StatusOK, response)
		return 
//		w.WriteHeader(http.StatusNotFound)
//		return
	} else {
        	fmt.Println("Get intance with id "+instanceId)
		fmt.Println(instance)

		state, err = c.cloudClient.GetInstanceState(instance.InternalId)
		if err != nil {
			fmt.Println(err)
	//		w.WriteHeader(http.StatusInternalServerError)
	//		return
		}
	}

	fmt.Println("The instance state is: "+state)

	if state == "pending" {
		instance.LastOperation.State = "in progress"
		instance.LastOperation.Description = "creating service instance..."
	} else if state == "running" {
		instance.LastOperation.State = "succeeded"
		instance.LastOperation.Description = "successfully created service instance"
//	} else if state == "unknown" {
//		*instance = model.ServiceInstance{
//			Id: state,
//			LastOperation: &failedOperation,
//		}
	} else {
		instance.LastOperation.State = "failed"
		instance.LastOperation.Description = "failed to create service instance"
	}

//	response := model.CreateServiceInstanceResponse{
//		DashboardUrl:  instance.DashboardUrl,
//		LastOperation: instance.LastOperation,
//	}
	response := instance.LastOperation
	utils.WriteResponse(w, http.StatusOK, response)
}

func (c *Controller) RemoveServiceInstance(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Remove Service Instance...")

	instanceId := utils.ExtractVarsFromRequest(r, "service_instance_guid")
	instance := c.instanceMap[instanceId]
	if instance == nil {
		fmt.Println("No instance found with id: "+instanceId)
		w.WriteHeader(http.StatusGone)
		return
	}

	err := c.cloudClient.DeleteInstance(instance.InternalId)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	delete(c.instanceMap, instanceId)
	utils.MarshalAndRecord(c.instanceMap, conf.DataPath, conf.ServiceInstancesFileName)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	err = c.deleteAssociatedBindings(instanceId)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	fmt.Println(instance)
	utils.WriteResponse(w, http.StatusOK, instance)
}

func (c *Controller) Bind(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Bind Service Instance...")

	bindingId := utils.ExtractVarsFromRequest(r, "service_binding_guid")
	instanceId := utils.ExtractVarsFromRequest(r, "service_instance_guid")

	instance := c.instanceMap[instanceId]
	if instance == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	ip, userName, privateKey, err := c.cloudClient.InjectKeyPair(instance.InternalId)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	credential := model.Credential{
		PublicIP:   ip,
		UserName:   userName,
		PrivateKey: privateKey,
	}

	response := model.CreateServiceBindingResponse{
		Credentials: credential,
	}

	c.bindingMap[bindingId] = &model.ServiceBinding{
		Id:                bindingId,
		ServiceId:         instance.ServiceId,
		ServicePlanId:     instance.PlanId,
		PrivateKey:        privateKey,
		ServiceInstanceId: instance.Id,
	}

	err = utils.MarshalAndRecord(c.bindingMap, conf.DataPath, conf.ServiceBindingsFileName)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	utils.WriteResponse(w, http.StatusCreated, response)
}

func (c *Controller) UnBind(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Unbind Service Instance...")

	bindingId := utils.ExtractVarsFromRequest(r, "service_binding_guid")
	instanceId := utils.ExtractVarsFromRequest(r, "service_instance_guid")
	instance := c.instanceMap[instanceId]
	if instance == nil {
		w.WriteHeader(http.StatusGone)
		return
	}

	err := c.cloudClient.RevokeKeyPair(instance.InternalId, c.bindingMap[bindingId].PrivateKey)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	delete(c.bindingMap, bindingId)
	err = utils.MarshalAndRecord(c.bindingMap, conf.DataPath, conf.ServiceBindingsFileName)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	utils.WriteResponse(w, http.StatusOK, "{}")
}

// Private instance methods

func (c *Controller) deleteAssociatedBindings(instanceId string) error {
	for id, binding := range c.bindingMap {
		if binding.ServiceInstanceId == instanceId {
			delete(c.bindingMap, id)
		}
	}

	return utils.MarshalAndRecord(c.bindingMap, conf.DataPath, conf.ServiceBindingsFileName)
}

// Private methods

func createCloudClient(cloudName string) (client.Client, error) {
	switch cloudName {
		case utils.AWS:
			return client.NewAWSClient("eu-west-1"), nil

		case utils.SOFTLAYER, utils.SL:
			return client.NewSoftLayerClient(), nil
	}

	return nil, errors.New(fmt.Sprintf("Invalid cloud name: %s", cloudName))
}
