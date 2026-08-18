package example.booking;

import java.time.Instant;
import static java.util.Objects.requireNonNull;

@ApplicationScoped
@Path("/bookings")
public class BookingService extends BaseService implements Auditable {
    @ConfigProperty(name = "booking.timeout")
    String timeout;

    public Booking create(String userId) throws BookingException {
        return repository.save(new Booking(userId));
    }

    public void configure() {
        String region = System.getenv("APP_REGION");
        String timeoutValue = System.getProperty("booking.timeout");
    }

    private void fail() {
        throw new BookingException("invalid booking");
    }
}
