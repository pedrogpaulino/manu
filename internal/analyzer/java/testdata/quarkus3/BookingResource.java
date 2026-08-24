package com.example.booking;

import jakarta.ws.rs.DELETE;
import jakarta.ws.rs.GET;
import jakarta.ws.rs.POST;
import jakarta.ws.rs.Path;
import jakarta.ws.rs.core.MediaType;
import jakarta.ws.rs.core.Response;
import org.eclipse.microprofile.config.inject.ConfigProperty;

@Path("/api//bookings")
public class BookingResource {
    @ConfigProperty(name = "booking.table")
    String table;

    @GET
    @Path("list")
    public Response list() {
        return Response.ok(table).build();
    }

    @POST
    @Path("/create")
    public Response create() {
        return Response.status(Response.Status.CREATED).build();
    }

    @DELETE
    public Response remove() {
        return Response.noContent().build();
    }

    String sourceString = "@Path(\"/fake\") @GET";
    // @Path("/comment")
}
