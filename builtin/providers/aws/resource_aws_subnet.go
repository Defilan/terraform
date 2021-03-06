package aws

import (
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/hashicorp/terraform/helper/resource"
	"github.com/hashicorp/terraform/helper/schema"
)

func resourceAwsSubnet() *schema.Resource {
	return &schema.Resource{
		Create: resourceAwsSubnetCreate,
		Read:   resourceAwsSubnetRead,
		Update: resourceAwsSubnetUpdate,
		Delete: resourceAwsSubnetDelete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		SchemaVersion: 1,
		MigrateState:  resourceAwsSubnetMigrateState,

		Schema: map[string]*schema.Schema{
			"vpc_id": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"cidr_block": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"ipv6_cidr_block": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
			},

			"availability_zone": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},

			"map_public_ip_on_launch": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},

			"assign_ipv6_address_on_creation": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},

			"ipv6_cidr_block_association_id": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"tags": tagsSchema(),
		},
	}
}

func resourceAwsSubnetCreate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).ec2conn

	createOpts := &ec2.CreateSubnetInput{
		AvailabilityZone: aws.String(d.Get("availability_zone").(string)),
		CidrBlock:        aws.String(d.Get("cidr_block").(string)),
		VpcId:            aws.String(d.Get("vpc_id").(string)),
	}

	if v, ok := d.GetOk("ipv6_cidr_block"); ok {
		createOpts.Ipv6CidrBlock = aws.String(v.(string))
	}

	var err error
	resp, err := conn.CreateSubnet(createOpts)

	if err != nil {
		return fmt.Errorf("Error creating subnet: %s", err)
	}

	// Get the ID and store it
	subnet := resp.Subnet
	d.SetId(*subnet.SubnetId)
	log.Printf("[INFO] Subnet ID: %s", *subnet.SubnetId)

	// Wait for the Subnet to become available
	log.Printf("[DEBUG] Waiting for subnet (%s) to become available", *subnet.SubnetId)
	stateConf := &resource.StateChangeConf{
		Pending: []string{"pending"},
		Target:  []string{"available"},
		Refresh: SubnetStateRefreshFunc(conn, *subnet.SubnetId),
		Timeout: 10 * time.Minute,
	}

	_, err = stateConf.WaitForState()

	if err != nil {
		return fmt.Errorf(
			"Error waiting for subnet (%s) to become ready: %s",
			d.Id(), err)
	}

	return resourceAwsSubnetUpdate(d, meta)
}

func resourceAwsSubnetRead(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).ec2conn

	resp, err := conn.DescribeSubnets(&ec2.DescribeSubnetsInput{
		SubnetIds: []*string{aws.String(d.Id())},
	})

	if err != nil {
		if ec2err, ok := err.(awserr.Error); ok && ec2err.Code() == "InvalidSubnetID.NotFound" {
			// Update state to indicate the subnet no longer exists.
			d.SetId("")
			return nil
		}
		return err
	}
	if resp == nil {
		return nil
	}

	subnet := resp.Subnets[0]

	d.Set("vpc_id", subnet.VpcId)
	d.Set("availability_zone", subnet.AvailabilityZone)
	d.Set("cidr_block", subnet.CidrBlock)
	d.Set("map_public_ip_on_launch", subnet.MapPublicIpOnLaunch)
	d.Set("assign_ipv6_address_on_creation", subnet.AssignIpv6AddressOnCreation)
	for _, a := range subnet.Ipv6CidrBlockAssociationSet {
		if *a.Ipv6CidrBlockState.State == "associated" { //we can only ever have 1 IPv6 block associated at once
			d.Set("ipv6_cidr_block_association_id", a.AssociationId)
			d.Set("ipv6_cidr_block", a.Ipv6CidrBlock)
		} else {
			d.Set("ipv6_cidr_block_association_id", "") // we blank these out to remove old entries
			d.Set("ipv6_cidr_block", "")
		}
	}
	d.Set("tags", tagsToMap(subnet.Tags))

	return nil
}

func resourceAwsSubnetUpdate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).ec2conn

	d.Partial(true)

	if err := setTags(conn, d); err != nil {
		return err
	} else {
		d.SetPartial("tags")
	}

	if d.HasChange("assign_ipv6_address_on_creation") {
		modifyOpts := &ec2.ModifySubnetAttributeInput{
			SubnetId: aws.String(d.Id()),
			AssignIpv6AddressOnCreation: &ec2.AttributeBooleanValue{
				Value: aws.Bool(d.Get("assign_ipv6_address_on_creation").(bool)),
			},
		}

		log.Printf("[DEBUG] Subnet modify attributes: %#v", modifyOpts)

		_, err := conn.ModifySubnetAttribute(modifyOpts)

		if err != nil {
			return err
		} else {
			d.SetPartial("assign_ipv6_address_on_creation")
		}
	}

	if d.HasChange("map_public_ip_on_launch") {
		modifyOpts := &ec2.ModifySubnetAttributeInput{
			SubnetId: aws.String(d.Id()),
			MapPublicIpOnLaunch: &ec2.AttributeBooleanValue{
				Value: aws.Bool(d.Get("map_public_ip_on_launch").(bool)),
			},
		}

		log.Printf("[DEBUG] Subnet modify attributes: %#v", modifyOpts)

		_, err := conn.ModifySubnetAttribute(modifyOpts)

		if err != nil {
			return err
		} else {
			d.SetPartial("map_public_ip_on_launch")
		}
	}

	d.Partial(false)

	return resourceAwsSubnetRead(d, meta)
}

func resourceAwsSubnetDelete(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).ec2conn

	log.Printf("[INFO] Deleting subnet: %s", d.Id())
	req := &ec2.DeleteSubnetInput{
		SubnetId: aws.String(d.Id()),
	}

	wait := resource.StateChangeConf{
		Pending:    []string{"pending"},
		Target:     []string{"destroyed"},
		Timeout:    10 * time.Minute,
		MinTimeout: 1 * time.Second,
		Refresh: func() (interface{}, string, error) {
			_, err := conn.DeleteSubnet(req)
			if err != nil {
				if apiErr, ok := err.(awserr.Error); ok {
					if apiErr.Code() == "DependencyViolation" {
						// There is some pending operation, so just retry
						// in a bit.
						return 42, "pending", nil
					}

					if apiErr.Code() == "InvalidSubnetID.NotFound" {
						return 42, "destroyed", nil
					}
				}

				return 42, "failure", err
			}

			return 42, "destroyed", nil
		},
	}

	if _, err := wait.WaitForState(); err != nil {
		return fmt.Errorf("Error deleting subnet: %s", err)
	}

	return nil
}

// SubnetStateRefreshFunc returns a resource.StateRefreshFunc that is used to watch a Subnet.
func SubnetStateRefreshFunc(conn *ec2.EC2, id string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		resp, err := conn.DescribeSubnets(&ec2.DescribeSubnetsInput{
			SubnetIds: []*string{aws.String(id)},
		})
		if err != nil {
			if ec2err, ok := err.(awserr.Error); ok && ec2err.Code() == "InvalidSubnetID.NotFound" {
				resp = nil
			} else {
				log.Printf("Error on SubnetStateRefresh: %s", err)
				return nil, "", err
			}
		}

		if resp == nil {
			// Sometimes AWS just has consistency issues and doesn't see
			// our instance yet. Return an empty state.
			return nil, "", nil
		}

		subnet := resp.Subnets[0]
		return subnet, *subnet.State, nil
	}
}
