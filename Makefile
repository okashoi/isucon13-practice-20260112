APPNAME := isupipe-go.service

.PHONY: *
gogo: stop-services build logs/clear start-services

stop-services:
	sudo systemctl stop dnsdist
	sudo systemctl stop pdns
	sudo systemctl stop nginx
	sudo systemctl stop $(APPNAME)
	ssh isucon-s3 "sudo systemctl stop $(APPNAME)"
	ssh isucon-s2 "sudo systemctl stop mysql"
	# ssh isucon-s2 "sudo systemctl stop mysql"

build:
	cd go && make
	scp go/isupipe isucon-s3:/home/isucon/webapp/go/isupipe

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
	sudo systemctl start $(APPNAME)
	ssh isucon-s3 "sudo systemctl start $(APPNAME)"
	sudo systemctl start nginx
	sudo systemctl start pdns
	sudo systemctl start dnsdist