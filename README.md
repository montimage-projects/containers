# Containers
This repositori contain divers Docker containers which can be used for different purposes, demos.

These containers are publically accessible. They have same name but differ by its tag.


# Add new container

To add and push a new container, you will neeed to:
- create a new directory
- put your source code, assets, ..., and Dockerfile there
- add an action in `.github/workflows/docker-build.yml` to build and push the container