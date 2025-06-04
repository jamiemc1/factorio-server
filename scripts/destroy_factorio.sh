#!/bin/bash
temporaryLog="/tmp/apps.log"
fly apps list | cut -f1 | sed '/NAME/d;/^$/d' > ${temporaryLog}

while read app
do
  fly apps destroy $app --yes
done < ${temporaryLog}

rm ${temporaryLog}
