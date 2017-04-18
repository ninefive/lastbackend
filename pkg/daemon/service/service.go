//
// Last.Backend LLC CONFIDENTIAL
// __________________
//
// [2014] - [2017] Last.Backend LLC
// All Rights Reserved.
//
// NOTICE:  All information contained herein is, and remains
// the property of Last.Backend LLC and its suppliers,
// if any.  The intellectual and technical concepts contained
// herein are proprietary to Last.Backend LLC
// and its suppliers and may be covered by Russian Federation and Foreign Patents,
// patents in process, and are protected by trade secret or copyright law.
// Dissemination of this information or reproduction of this material
// is strictly forbidden unless prior written permission is obtained
// from Last.Backend LLC.
//

package service

import (
	"context"
	"github.com/lastbackend/lastbackend/pkg/apis/types"
	ctx "github.com/lastbackend/lastbackend/pkg/daemon/context"
	"github.com/lastbackend/lastbackend/pkg/daemon/node"
	"github.com/lastbackend/lastbackend/pkg/daemon/service/routes/request"
	"github.com/lastbackend/lastbackend/pkg/daemon/storage/store"
	"github.com/satori/go.uuid"
	"strings"
	"time"
)

type service struct {
	Context   context.Context
	Namespace types.Meta
}

func New(ctx context.Context, namespace types.Meta) *service {
	return &service{
		Context:   ctx,
		Namespace: namespace,
	}
}

func (s *service) List() (types.ServiceList, error) {
	var (
		storage = ctx.Get().GetStorage()
		list    = types.ServiceList{}
	)

	items, err := storage.Service().ListByNamespace(s.Context, s.Namespace.ID)
	if err != nil {
		return list, err
	}

	for _, item := range items {
		var service = item
		list = append(list, service)
	}

	return list, nil
}

func (s *service) Get(service string) (*types.Service, error) {

	var (
		log     = ctx.Get().GetLogger()
		storage = ctx.Get().GetStorage()
	)

	svc, err := storage.Service().GetByName(s.Context, s.Namespace.ID, service)

	if err != nil {
		log.Error("Error: find service by name", err.Error())
		return nil, err
	}

	return svc, nil
}

func (s *service) Create(rq *request.RequestServiceCreateS) (*types.Service, error) {

	var (
		log     = ctx.Get().GetLogger()
		storage = ctx.Get().GetStorage()
		svc     = types.Service{}
	)

	log.Debug("Service: create new service")

	svc.Meta = types.ServiceMeta{}
	svc.Meta.SetDefault()

	svc.Meta.Name = rq.Name
	svc.Meta.Region = rq.Region
	svc.Meta.Namespace = s.Namespace.Name
	svc.Meta.Description = rq.Description

	svc.Meta.Replicas = 1
	svc.Pods = make(map[string]*types.Pod)

	if rq.Replicas != nil && *rq.Replicas > 0 {
		svc.Meta.Replicas = *rq.Replicas
	}

	config, err := createConfig(rq.Config)
	if err != nil {
		log.Errorf("Error: create config from request opts : %s", err.Error())
		return &svc, err
	}

	svc.Config = *config

	log.Debugf("Service: Create: add pods : %d", svc.Meta.Replicas)
	for i := 0; i < svc.Meta.Replicas; i++ {
		log.Debug("Service: Create: add new pod")
		if err := s.AddPod(&svc); err != nil {
			log.Errorf("Service: Create: add new pod error: %s", err.Error())
			return &svc, err
		}
	}

	if err = storage.Service().Insert(s.Context, &svc); err != nil {
		log.Errorf("Error: insert service to db : %s", err.Error())
		return &svc, err
	}

	return &svc, nil
}

func (s *service) Update(service *types.Service, rq *request.RequestServiceUpdateS) error {

	var (
		err     error
		log     = ctx.Get().GetLogger()
		storage = ctx.Get().GetStorage()
	)

	log.Debug("Service: update service info and config")

	if rq.Name != "" {
		service.Meta.Name = rq.Name
	}

	if rq.Description != "" {
		service.Meta.Description = rq.Description
	}

	if rq.Domains != nil {
		service.Domains = rq.Domains
	}

	if rq.Replicas != nil {
		log.Debugf("Service: Update: set replicas: %d", *rq.Replicas)
		service.Meta.Replicas = *rq.Replicas
	}

	if rq.Config != nil {
		if err := updateConfig(rq.Config, &service.Config); err != nil {
			log.Error("Error: update service config from request opts", err)
			return err
		}

		// Update pod spec
		spec := s.GenerateSpec(service)

		log.Debugf("Service: Update: pods count: %d", len(service.Pods))
		for _, pod := range service.Pods {
			log.Debugf("Service: Update: pod %s update", pod.Meta.ID)
			pod.State.State = "running"
			pod.Spec = spec
		}

	}

	s.Scale(s.Context, service)

	if err = storage.Service().Update(s.Context, service); err != nil {
		log.Error("Error: insert service to db", err)
		return err
	}

	return nil
}

func (s *service) Remove(service *types.Service) error {
	var (
		log     = ctx.Get().GetLogger()
		storage = ctx.Get().GetStorage()
	)

	service.State.State = "deleting"

	if len(service.Pods) == 0 {
		if err := storage.Service().Remove(s.Context, service); err != nil {
			log.Error("Error: insert service to db", err)
			return err
		}
		return nil
	}

	for _, pod := range service.Pods {
		pod.State.State = "deleting"
	}

	if err := storage.Service().Update(s.Context, service); err != nil {
		log.Error("Error: insert service to db", err)
		return err
	}
	return nil
}

func (s *service) AddPod(service *types.Service) error {

	var (
		log = ctx.Get().GetLogger()
	)

	log.Debug("Create new pod state on service")

	pod := types.Pod{}

	pod.Meta.ID = uuid.NewV4().String()
	pod.Meta.Created = time.Now()
	pod.Meta.Updated = time.Now()
	pod.State.State = "running"

	if len(service.Pods) > 0 {
		for _, p := range service.Pods {
			pod.Spec = p.Spec
			break
		}
	} else {
		pod.Spec = s.GenerateSpec(service)
	}

	n, err := node.New().Allocate(s.Context, pod.Spec)
	if err != nil {
		return err
	}

	log.Debugf("Service: Add pod: Node meta: %s", n.Meta)
	pod.Meta.Hostname = n.Meta.Hostname
	service.Pods[pod.Meta.ID] = &pod

	return nil
}

func (s *service) DelPod(service *types.Service) error {

	var (
		log = ctx.Get().GetLogger()
	)

	log.Debug("Delete pod service")

	for _, pod := range service.Pods {
		if pod.State.State != "deleting" {
			log.Debugf("Mark pod for deletion: %s", pod.Meta.ID)
			pod.State.State = "deleting"
			break
		}
	}

	return nil
}

func (s *service) SetPods(c context.Context, pods []types.Pod) error {
	var (
		log     = ctx.Get().GetLogger()
		storage = ctx.Get().GetStorage()
	)

	for _, pod := range pods {
		log.Debugf("update pod state: %s", pod.Meta.ID)
		svc, err := storage.Service().GetByPodID(c, pod.Meta.ID)
		if err != nil {
			log.Errorf("Error: get service by pod ID %s from db: %s", pod.Meta.ID, err.Error())
			if err.Error() == store.ErrKeyNotFound {
				continue
			}
			return err
		}

		p, e := storage.Pod().GetByID(c, svc.Meta.Namespace, svc.Meta.ID, pod.Meta.ID)

		if e != nil {
			log.Errorf("Error: get pod from db: %s", e.Error())
			continue
		}

		p.Containers = pod.Containers
		p.State = pod.State

		if p.State.State == "deleted" {
			log.Debugf("Service: Set pods: remove deleted pod: %s", p.Meta.ID)
			if err := storage.Pod().Remove(c, svc.Meta.Namespace, svc.Meta.ID, p); err != nil {
				log.Errorf("Error: set pod to db: %s", err)
				return err
			}
			delete(svc.Pods, p.Meta.ID)

			if len(svc.Pods) == 0 && svc.State.State == "deleting" {
				storage.Service().Remove(c, svc)
			}

			return nil
		}

		if err := storage.Pod().Update(c, svc.Meta.Namespace, svc.Meta.ID, p); err != nil {
			log.Errorf("Error: set pod to db: %s", err)
			return err
		}
	}

	return nil
}

func (s *service) Scale(c context.Context, service *types.Service) error {
	var (
		log      = ctx.Get().GetLogger()
		replicas int
	)

	for _, pod := range service.Pods {
		if pod.State.State != "deleting" {
			replicas++
		}
	}

	log.Debugf("Service: Scale: current replicas: %d", replicas)

	if replicas == service.Meta.Replicas {
		log.Debug("Service: Replicas not needed, replicas equal")
		return nil
	}

	if replicas < service.Meta.Replicas {
		log.Debug("Service: Replicas: create a new replicas")
		for i := 0; i < (service.Meta.Replicas - replicas); i++ {
			if err := s.AddPod(service); err != nil {
				return err
			}
		}
	}

	if replicas > service.Meta.Replicas {
		log.Debug("Service: Replicas: remove  unneeded replicas")
		for i := 0; i < (replicas - service.Meta.Replicas); i++ {
			if err := s.DelPod(service); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *service) GenerateSpec(service *types.Service) types.PodSpec {

	var (
		log = ctx.Get().GetLogger()
	)

	log.Debug("Generate new node pod spec")
	var spec = types.PodSpec{}
	spec.ID = uuid.NewV4().String()
	spec.Created = time.Now()
	spec.Updated = time.Now()

	cs := new(types.ContainerSpec)
	cs.Image = types.ImageSpec{
		Name: service.Config.Image,
		Pull: true,
	}

	for _, port := range service.Config.Ports {
		cs.Ports = append(cs.Ports, types.ContainerPortSpec{
			ContainerPort: port.Container,
			Protocol:      port.Protocol,
		})
	}

	cs.Command = service.Config.Command
	cs.Entrypoint = service.Config.Entrypoint
	cs.Envs = service.Config.EnvVars
	cs.Quota = types.ContainerQuotaSpec{
		Memory: service.Config.Memory,
	}

	cs.RestartPolicy = types.ContainerRestartPolicySpec{
		Name:    "always",
		Attempt: 0,
	}

	spec.Containers = append(spec.Containers, cs)

	var state = new(types.PodState)
	state.State = service.State.State

	return spec
}

func createConfig(opts *request.ServiceConfig) (*types.ServiceConfig, error) {
	config := new(types.ServiceConfig)
	if err := patchConfig(opts, config); err != nil {
		return nil, err
	}
	return config, nil
}

func updateConfig(opts *request.ServiceConfig, config *types.ServiceConfig) error {
	if config == nil {
		config = new(types.ServiceConfig)
	}
	return patchConfig(opts, config)
}

func patchConfig(opts *request.ServiceConfig, config *types.ServiceConfig) error {

	config.Memory = int64(32)

	if opts.Memory != nil {
		config.Memory = *opts.Memory
	}

	if opts.Command != nil {
		config.Command = strings.Split(*opts.Command, " ")
	}

	if opts.Image != nil {
		config.Image = *opts.Image
	}

	if opts.Entrypoint != nil {
		config.Entrypoint = strings.Split(*opts.Entrypoint, " ")
	}

	if opts.EnvVars != nil {
		config.EnvVars = *opts.EnvVars
	}

	if opts.Ports != nil {
		config.Ports = []types.Port{}
		for _, val := range *opts.Ports {
			config.Ports = append(config.Ports, types.Port{
				Protocol:  val.Protocol,
				Container: val.Internal,
				Host:      val.External,
				Published: val.Published,
			})
		}
	}

	return nil
}
