FROM busybox:1.36
COPY run.sh /run.sh
RUN chmod 0755 /run.sh
VOLUME ["/state"]
CMD ["/run.sh"]
