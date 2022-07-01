package graphql

// This file will be automatically regenerated based on the schema, any resolver implementations
// will be copied through when generating and any unknown code will be moved to the end.

import (
	"context"
	"fmt"

	"github.com/evergreen-ci/evergreen/model/host"
	restModel "github.com/evergreen-ci/evergreen/rest/model"
)

func (r *volumeResolver) Host(ctx context.Context, obj *restModel.APIVolume) (*restModel.APIHost, error) {
	if obj.HostID == nil || *obj.HostID == "" {
		return nil, nil
	}
	host, err := host.FindOneId(*obj.HostID)
	if err != nil {
		return nil, InternalServerError.Send(ctx, fmt.Sprintf("Error finding host %s: %s", *obj.HostID, err.Error()))
	}
	if host == nil {
		return nil, ResourceNotFound.Send(ctx, fmt.Sprintf("Unable to find host %s", *obj.HostID))
	}
	apiHost := restModel.APIHost{}
	err = apiHost.BuildFromService(host)
	if err != nil {
		return nil, InternalServerError.Send(ctx, fmt.Sprintf("Error building apiHost %s from service: %s", host.Id, err))
	}
	return &apiHost, nil
}

// Volume returns VolumeResolver implementation.
func (r *Resolver) Volume() VolumeResolver { return &volumeResolver{r} }

type volumeResolver struct{ *Resolver }