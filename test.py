import boto3
import random
import bisect

from datetime import datetime

# now = datetime.now()


# client = boto3.client('ec2',region_name='eu-central-1')

# prices=client.describe_spot_price_history(
#     InstanceTypes=['m3.medium', 'c5.large'],
#     StartTime=datetime.now())
    # MaxResults=1,
    # ProductDescriptions=['Linux/UNIX (Amazon VPC)'],
    # AvailabilityZone='us-east-1a'

# print(prices['SpotPriceHistory'])

# products = [
# 'Linux/UNIX',
#  'Red Hat Enterprise Linux',
#  'SUSE Linux',
#  'Windows']

# res = [ '_'.join(p.lower().replace('/',' ').split()) for p in products]

# print(res)


l={}

items = list(range(5))
random.shuffle(items)

for i in items:
    l.setdefault('foo', [])
    bisect.insort(l['foo'], i)

print(l)
