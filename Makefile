APPNAME := isupipe-go.service
ISUCON3_HOST := isucon-s3
ISUCON3_DEST := /home/isucon/webapp/go/isupipe

.PHONY: *
gogo: stop-services build logs/clear start-services

stop-services:
	sudo systemctl stop nginx
	ssh $(ISUCON3_HOST) "sudo systemctl stop $(APPNAME)"
	ssh isucon-s2 "sudo systemctl stop mysql"
	# ssh isucon-s2 "sudo systemctl stop mysql"

build:
	cd go && make
	scp go/isupipe $(ISUCON3_HOST):$(ISUCON3_DEST)

logs: limit=10000
logs: opts=
logs:
	journalctl -ex --since "$(shell systemctl status isupipe-go.service | grep "Active:" | awk '{print $$6, $$7}')" -n $(limit) -q $(opts)

logs/error:
	$(MAKE) logs opts='--grep "(error|panic|- 500)" --no-pager'

logs/clear:
	sudo journalctl --rotate && sudo journalctl --vacuum-size=1K
	sudo truncate --size 0 /var/log/nginx/access.log
	sudo truncate --size 0 /var/log/nginx/error.log
	ssh isucon-s2 "sudo truncate --size 0 /var/log/mysql/mysql-slow.log && sudo chmod 666 /var/log/mysql/mysql-slow.log"
	ssh isucon-s2 "sudo truncate --size 0 /var/log/mysql/error.log"

start-services:
	sudo systemctl daemon-reload
	# ssh isucon-s2 "sudo systemctl start mysql"
	ssh isucon-s2 "sudo systemctl start mysql"
	ssh $(ISUCON3_HOST) "sudo systemctl start $(APPNAME)"
	sudo systemctl start nginx
