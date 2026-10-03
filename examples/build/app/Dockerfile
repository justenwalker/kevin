FROM busybox:stable

ARG GREETING=hello
RUN echo "${GREETING} from a built image" > /www.txt
CMD ["sh", "-c", "cat /www.txt && exec httpd -f -p 8080 -h /"]
